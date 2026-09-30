package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gatt/internal/app"

	"github.com/mattn/go-sqlite3"
)

type updateSnapshotFile struct {
	Path string `json:"path"`
	packageFile
}

type updateSnapshotManifest struct {
	Format    string               `json:"format"`
	BuildID   string               `json:"build_id"`
	Schema    int                  `json:"data_schema"`
	CreatedAt time.Time            `json:"created_at"`
	Files     []updateSnapshotFile `json:"files"`
}

// The old process's drain lease must remain held throughout this copy. SQLite's
// backup API includes committed WAL; private refs keep their relative paths.
// This local rollback checkpoint is not a portable or age-encrypted backup.
func updateSQLiteSnapshot(ctx context.Context, sourcePath, targetPath string) error {
	uri := &url.URL{Scheme: "file", Path: filepath.ToSlash(sourcePath)}
	uri.RawQuery = url.Values{"mode": {"ro"}}.Encode()
	source, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(targetPath)+"?_journal_mode=DELETE&_foreign_keys=on")
	if err != nil {
		return err
	}
	defer target.Close()
	src, err := source.Conn(ctx)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := target.Conn(ctx)
	if err != nil {
		return err
	}
	defer dst.Close()
	err = src.Raw(func(from any) error {
		return dst.Raw(func(to any) error {
			fromConn, sourceOK := from.(*sqlite3.SQLiteConn)
			toConn, targetOK := to.(*sqlite3.SQLiteConn)
			if !sourceOK || !targetOK {
				return errors.New("更新快照需要 SQLite backup API")
			}
			backup, err := toConn.Backup("main", fromConn, "main")
			if err != nil {
				return err
			}
			for {
				if err = ctx.Err(); err != nil {
					return errors.Join(err, backup.Close())
				}
				done, stepErr := backup.Step(128)
				if stepErr != nil {
					return errors.Join(stepErr, backup.Close())
				}
				if done {
					return backup.Finish()
				}
				select {
				case <-ctx.Done():
					return errors.Join(ctx.Err(), backup.Close())
				case <-time.After(time.Millisecond):
				}
			}
		})
	})
	if err != nil {
		return err
	}
	if err = dst.Close(); err != nil {
		return err
	}
	var integrity string
	if err = target.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("更新快照数据库完整性未确认")
	}
	rows, err := target.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	invalid := rows.Next()
	rowErr := rows.Err()
	rows.Close()
	if invalid || rowErr != nil {
		return errors.New("更新快照数据库引用完整性未确认")
	}
	if err = target.Close(); err != nil {
		return err
	}
	return os.Chmod(targetPath, 0600)
}

type updateCopyReader struct {
	ctx   context.Context
	input io.Reader
}

func (r updateCopyReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(p)
}

func updateCopySnapshotFile(ctx context.Context, from, to string, remaining *int64) (packageFile, error) {
	var result packageFile
	info, err := os.Lstat(from)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.New("更新快照仅允许普通文件，不追踪链接")
	}
	if info.Size() > *remaining {
		return result, errors.New("更新快照超过 2 GiB 上限")
	}
	if err = os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return result, err
	}
	input, err := os.Open(from)
	if err != nil {
		return result, err
	}
	defer input.Close()
	output, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	n, copyErr := io.Copy(output, io.LimitReader(updateCopyReader{ctx, input}, info.Size()+1))
	if copyErr == nil && n != info.Size() {
		copyErr = errors.New("更新快照文件在复制中改变")
	}
	if copyErr == nil {
		copyErr = output.Sync()
	}
	copyErr = errors.Join(copyErr, output.Close())
	if copyErr != nil {
		return result, copyErr
	}
	result.SHA256, err = fileSHA(to)
	result.Size = n
	*remaining -= n
	return result, err
}

func createUpdateSnapshot(ctx context.Context, journal updateJournal) (result string, resultErr error) {
	if relative, err := filepath.Rel(journal.DataDir, journal.Stage); err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("更新暂存必须位于数据目录外，拒绝递归快照")
	}
	folder := filepath.Join(journal.Stage, "data-snapshot")
	if err := os.Mkdir(folder, 0700); err != nil {
		return "", err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			resultErr = errors.Join(resultErr, os.RemoveAll(folder))
		}
	}()
	manifest := updateSnapshotManifest{Format: "cove-update-snapshot-v1", BuildID: journal.OldBuildID, Schema: journal.OldSchema, CreatedAt: time.Now().UTC(), Files: []updateSnapshotFile{}}
	remaining := int64(2 << 30)
	add := func(relative, source string) error {
		if len(manifest.Files) >= 20000 {
			return errors.New("更新快照超过文件数上限")
		}
		meta, err := updateCopySnapshotFile(ctx, source, filepath.Join(folder, relative), &remaining)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, updateSnapshotFile{Path: filepath.ToSlash(relative), packageFile: meta})
		return nil
	}
	if err := add("runtime-config.json", journal.ConfigPath); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(journal.DataDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".resource-spool-") || strings.HasPrefix(name, ".native-operation-") {
			return "", errors.New("更新快照发现未清理请求临时文件；请先完成故障清理")
		}
		switch name {
		case "gatt.db":
			info, statErr := os.Lstat(filepath.Join(journal.DataDir, name))
			if statErr != nil || !info.Mode().IsRegular() {
				return "", errors.New("更新数据库不是普通文件")
			}
			if err = updateSQLiteSnapshot(ctx, filepath.Join(journal.DataDir, name), filepath.Join(folder, name)); err != nil {
				return "", err
			}
			snapshotInfo, err := os.Stat(filepath.Join(folder, name))
			if err != nil || snapshotInfo.Size() > remaining {
				return "", errors.New("更新数据库快照超过上限")
			}
			hash, err := fileSHA(filepath.Join(folder, name))
			if err != nil {
				return "", err
			}
			remaining -= snapshotInfo.Size()
			manifest.Files = append(manifest.Files, updateSnapshotFile{Path: name, packageFile: packageFile{SHA256: hash, Size: snapshotInfo.Size()}})
		case "secrets", ".operations", ".response-cache":
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return "", errors.New("更新私有引用目录不是普通目录")
			}
			if err = os.Mkdir(filepath.Join(folder, name), 0700); err != nil {
				return "", err
			}
			err = filepath.WalkDir(filepath.Join(journal.DataDir, name), func(source string, child fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if child.Type()&os.ModeSymlink != 0 {
					return errors.New("更新私有引用包含符号链接")
				}
				relative, err := filepath.Rel(journal.DataDir, source)
				if err != nil {
					return err
				}
				if child.IsDir() {
					return os.MkdirAll(filepath.Join(folder, relative), 0700)
				}
				return add(relative, source)
			})
			if err != nil {
				return "", err
			}
		case "gatt.db-wal", "gatt.db-shm", ".gatt.lock", "runtime.json", "service.log", "restore-launch.log":
			// WAL is included by SQLite backup. Runtime/lock/logs are not data refs.
		default:
			if filepath.Clean(filepath.Join(journal.DataDir, name)) == filepath.Clean(journal.ConfigPath) {
				continue
			}
			return "", errors.New("数据目录存在未审核条目，拒绝声称更新快照完整")
		}
	}
	if schema, err := readDataSchema(folder); err != nil || schema != journal.OldSchema {
		return "", errors.New("更新快照 schema 与旧数据不符")
	}
	if err = writePrivate(filepath.Join(folder, "manifest.json"), mustPlatformJSON(manifest)); err != nil {
		return "", err
	}
	var directories []string
	if err = filepath.WalkDir(folder, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return "", err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err = syncPlatformDirectory(directories[index]); err != nil {
			return "", err
		}
	}
	succeeded = true
	return folder, nil
}

func updateTargetEnvironment(env platformEnvironment, journal updateJournal) platformEnvironment {
	env.Executable = journal.Target
	return env
}

func updateStart(ctx context.Context, launcher *platformRestoreLauncher, journal updateJournal, buildID, binarySHA string, closed bool) error {
	env := launcher.Environment
	if err := launcher.noService(ctx, app.RestorePointer{ConfigPath: journal.ConfigPath, DataDir: journal.DataDir, BuildID: buildID}); err != nil {
		return err
	}
	if err := restoreStopped(env); err != nil {
		return err
	}
	if actual, err := fileSHA(journal.Target); err != nil || actual != binarySHA {
		return errors.New("更新启动 binary hash 不符")
	}
	args := []string{"-config", journal.ConfigPath, "-data-dir", journal.DataDir, "-background", "-admission-closed=" + fmt.Sprint(closed), "serve"}
	launcher.closed[journal.DataDir] = closed
	return launcher.Spawn(ctx, journal.Target, args, journal.DataDir)
}

// Called by update apply. The target CLI delegates to its verified prepared
// updater, so even Unix uses a distinct executable while replacing the target.
func (env platformEnvironment) applyUpdateSwitch(ctx context.Context, filename string, spawn platformRestoreSpawn) error {
	journal, err := readUpdateJournal(filename, env)
	if err != nil {
		return err
	}
	updaterName := "gatt-updater"
	if env.OS == "windows" {
		updaterName += ".exe"
	}
	if journal.Updater != filepath.Join(journal.Stage, updaterName) {
		return errors.New("受控 updater 路径不符")
	}
	updaterInfo, err := os.Lstat(journal.Updater)
	if err != nil || !updaterInfo.Mode().IsRegular() {
		return errors.New("受控 updater 必须是普通文件")
	}
	if actual, err := fileSHA(journal.Updater); err != nil || actual != journal.OldBinarySHA {
		return errors.New("受控 updater hash 与准备阶段不符")
	}
	if env.Executable == journal.Target {
		output, runErr := env.Run(ctx, journal.Updater, "-config", journal.ConfigPath, "-data-dir", journal.DataDir, "-background", "update", "apply", "--journal", filename)
		_, outputErr := env.Output.Write(output)
		if runErr != nil {
			if state, readErr := readUpdateJournal(filename, env); readErr == nil {
				runErr = fmt.Errorf("updater 失败，journal phase=%s（%s）: %w", state.Phase, state.Notice, runErr)
			}
		}
		return errors.Join(runErr, outputErr)
	}
	if env.Executable != journal.Updater || app.BuildID != journal.OldBuildID {
		return errors.New("更新只能由准备阶段已校验的旧版 updater 执行")
	}
	if spawn == nil {
		return errors.New("受控启动器未接入")
	}
	stageLock, err := lockDataDir(journal.Stage)
	if err != nil {
		return fmt.Errorf("更新 stage 已被其他 launcher 持有: %w", err)
	}
	defer stageLock.Close()
	env = updateTargetEnvironment(env, journal)
	launcher := &platformRestoreLauncher{Environment: env, Spawn: spawn, BinarySHA: journal.OldBinarySHA, closed: map[string]bool{}}
	old := app.RestorePointer{ConfigPath: journal.ConfigPath, DataDir: journal.DataDir, BuildID: journal.OldBuildID}
	newPointer := old
	newPointer.BuildID = journal.NewBuildID
	if journal.Phase == "succeeded" {
		liveEnv, record, status, err := launcher.verified(ctx, newPointer)
		if err != nil {
			return err
		}
		if !status.Ready {
			return errors.New("journal 已完成，但当前更新实例未就绪")
		}
		closed, err := restoreControl(ctx, liveEnv, record, "state", "")
		if err != nil {
			return err
		}
		if closed {
			return errors.New("journal 已完成，但当前更新实例准入仍关闭")
		}
		return nil
	}
	if journal.Phase != "prepared" {
		return errors.New("更新 journal 有未闭合阶段；请按 hash/PID/schema 检查恢复，不自动试运行半包")
	}
	if err = launcher.noService(ctx, old); err != nil {
		return err
	}
	if current, err := fileSHA(journal.Target); err != nil || current != journal.OldBinarySHA {
		return errors.New("准备后目标 binary 已改变；未 drain")
	}
	if current, err := fileSHA(journal.NewBinary); err != nil || current != journal.NewBinarySHA {
		return errors.New("准备后暂存 binary 已改变；未 drain")
	}
	if schema, err := readDataSchema(journal.DataDir); err != nil || schema != journal.OldSchema || schema != platformDataSchema || journal.DataSchema != platformDataSchema {
		return errors.New("自动更新仅适用于已初始化的 schema v2；未 drain，不初始化或迁移数据")
	}
	save := func(phase, notice string) error {
		journal.Phase, journal.Notice = phase, notice
		return writePrivate(filename, mustPlatformJSON(journal))
	}
	if err = save("draining", "准备一致快照，尚未停止旧实例"); err != nil {
		return err
	}
	drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	resume, err := launcher.drain(drainCtx, old)
	cancel()
	if err != nil {
		return errors.Join(err, save("failed_before_stop", "drain 未确认；旧 binary 未替换"))
	}
	resumeBeforeStop := func(cause error) error {
		if resume != nil {
			resume()
		}
		return errors.Join(cause, launcher.resumeError, save("failed_before_stop", "停止前失败；旧 binary 未替换，检查旧准入恢复确认"))
	}
	journal.Snapshot, err = createUpdateSnapshot(ctx, journal)
	if err != nil {
		return resumeBeforeStop(err)
	}
	if err = save("snapshot_ready", "一致私有数据快照已保存，尚未停止旧实例"); err != nil {
		return resumeBeforeStop(err)
	}
	if err = ctx.Err(); err != nil {
		return resumeBeforeStop(err)
	}
	if err = launcher.stop(ctx, old); err != nil {
		return errors.Join(err, save("stop_uncertain", "旧实例停止未确认；没有替换 binary"))
	}
	if err = save("old_stopped", "旧实例已停止，保留一致快照"); err != nil {
		return err
	}
	rollback := func(cause error) error {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		// Before touching binary/data, either prove no process or prove the exact
		// new instance still has closed admission. An open/unknown gate is unsafe.
		if stopErr := restoreStopped(env); stopErr != nil {
			liveEnv, record, _, verifyErr := launcher.verified(rollbackCtx, newPointer)
			if verifyErr != nil {
				return errors.Join(cause, verifyErr, save("rollback_blocked", "新实例身份/停止未确认，保留现场"))
			}
			closed, stateErr := restoreControl(rollbackCtx, liveEnv, record, "state", "")
			if stateErr != nil || !closed {
				return errors.Join(cause, stateErr, save("rollback_blocked", "新实例准入可能已开放，拒绝自动 binary/data 回退"))
			}
			if err := launcher.stop(rollbackCtx, newPointer); err != nil {
				return errors.Join(cause, err, save("rollback_blocked", "新实例停止未确认，拒绝回退"))
			}
		}
		if schema, err := readDataSchema(journal.DataDir); err != nil || schema != journal.OldSchema {
			return errors.Join(cause, err, save("rollback_blocked", "schema 已改变；必须旧 binary 与更新前快照一并恢复，拒绝仅回退 binary"))
		}
		if err := withStoppedUpdate(env, journal, func() error {
			current, err := fileSHA(journal.Target)
			if err != nil {
				return err
			}
			if current == journal.OldBinarySHA {
				return nil
			}
			if current != journal.NewBinarySHA {
				return errors.New("回退时 target hash 不明，拒绝覆盖")
			}
			backupSHA, err := fileSHA(journal.Backup)
			if err != nil || backupSHA != journal.OldBinarySHA {
				return errors.New("回退旧 binary backup hash 不符")
			}
			return copyPlatformFile(journal.Backup, journal.Target)
		}); err != nil {
			return errors.Join(cause, err, save("rollback_blocked", "旧 binary 回退失败，保留 snapshot/backup"))
		}
		if err := updateStart(rollbackCtx, launcher, journal, journal.OldBuildID, journal.OldBinarySHA, false); err != nil {
			return errors.Join(cause, err, save("rollback_failed", "旧 binary 已恢复，但旧实例启动失败"))
		}
		if err := launcher.ready(rollbackCtx, old); err != nil {
			return errors.Join(cause, err, save("rollback_failed", "旧 binary 已恢复，但旧实例 readiness 未确认"))
		}
		return errors.Join(fmt.Errorf("更新失败，旧实例已通过 readiness 恢复: %w", cause), save("rolled_back", "旧 binary/实例已恢复；当前数据与更新快照均保留，没有数据迁移或自动覆盖"))
	}
	if err = withStoppedUpdate(env, journal, func() error {
		if current, err := fileSHA(journal.Target); err != nil || current != journal.OldBinarySHA {
			return errors.New("停止后 target hash 改变")
		}
		if err := copyPlatformFile(journal.Target, journal.Backup); err != nil {
			return err
		}
		if err := save("backed_up", "旧 binary backup 已保存"); err != nil {
			return err
		}
		if err := copyPlatformFile(journal.NewBinary, journal.Target); err != nil {
			return err
		}
		if current, err := fileSHA(journal.Target); err != nil || current != journal.NewBinarySHA {
			return errors.New("替换后 target hash 未确认")
		}
		return save("replaced_pending_start", "binary 已替换，尚未关闭准入启动")
	}); err != nil {
		return rollback(err)
	}
	if err = updateStart(ctx, launcher, journal, journal.NewBuildID, journal.NewBinarySHA, true); err != nil {
		return rollback(err)
	}
	if err = save("new_started_closed", "新实例关闭准入启动，等待 readiness/build/PID 校验"); err != nil {
		return rollback(err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = launcher.ready(readyCtx, newPointer)
	cancel()
	if err != nil {
		return rollback(err)
	}
	if schema, err := readDataSchema(journal.DataDir); err != nil || schema != journal.OldSchema {
		return rollback(errors.New("新实例启动后 schema 改变，拒绝自动准入"))
	}
	if err = save("ready_closed", "新 build/readiness/关闭准入已确认，准备激活"); err != nil {
		return rollback(err)
	}
	if err = launcher.activate(ctx, newPointer); err != nil {
		return errors.Join(err, save("activation_uncertain", "准入开启结果未知；禁止自动回退数据，保留现场"))
	}
	if err = save("succeeded", "新实例 readiness/build/PID 已确认并开放准入；旧 binary 和一致数据快照保留"); err != nil {
		return fmt.Errorf("准入已开放但成功 journal 写入失败；禁止自动回退: %w", err)
	}
	return nil
}
