package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxLocalPackageBytes int64 = 256 << 20
const platformDataSchema = 2

type packageFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type localPackageManifest struct {
	Format         string                 `json:"format"`
	Version        string                 `json:"version"`
	BuildID        string                 `json:"build_id"`
	SourceID       string                 `json:"source_id"`
	GoVersion      string                 `json:"go_version"`
	NodeVersion    string                 `json:"node_version"`
	OS             string                 `json:"os"`
	Arch           string                 `json:"arch"`
	AdapterVersion string                 `json:"adapter_version"`
	WebEmbedded    bool                   `json:"web_embedded"`
	ManualOnly     bool                   `json:"manual_only"`
	License        string                 `json:"license"`
	MinDataSchema  int                    `json:"min_data_schema"`
	MaxDataSchema  int                    `json:"max_data_schema"`
	DataSchema     int                    `json:"data_schema"`
	Files          map[string]packageFile `json:"files"`
}
type updateJournal struct {
	Format       string    `json:"format"`
	Phase        string    `json:"phase"`
	PreparedAt   time.Time `json:"prepared_at"`
	Target       string    `json:"target"`
	ConfigPath   string    `json:"config_path"`
	DataDir      string    `json:"data_dir"`
	Listen       string    `json:"listen"`
	OldBinarySHA string    `json:"old_binary_sha256"`
	NewBinarySHA string    `json:"new_binary_sha256"`
	OldBuildID   string    `json:"old_build_id"`
	NewBuildID   string    `json:"new_build_id"`
	OldSchema    int       `json:"old_schema"`
	DataSchema   int       `json:"data_schema"`
	PackageSHA   string    `json:"package_sha256"`
	Stage        string    `json:"stage"`
	Snapshot     string    `json:"data_snapshot,omitempty"`
	Backup       string    `json:"backup"`
	NewBinary    string    `json:"new_binary"`
	Updater      string    `json:"updater"`
	Notice       string    `json:"notice"`
}

func validSHA(v string) bool {
	bytes, err := hex.DecodeString(v)
	return err == nil && len(bytes) == sha256.Size && v == strings.ToLower(v)
}
func packagePath(name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") || strings.HasPrefix(name, "/") || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("包内路径无效")
	}
	return filepath.FromSlash(name), nil
}
func readDataSchema(dir string) (int, error) {
	filename := filepath.Join(dir, "gatt.db")
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	uriPath := filepath.ToSlash(filename)
	if filepath.VolumeName(filename) != "" {
		uriPath = "/" + uriPath
	}
	uri := &url.URL{Scheme: "file", Path: uriPath}
	query := url.Values{"mode": {"ro"}, "_query_only": {"on"}}
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var schema int
	err = db.QueryRow("PRAGMA user_version").Scan(&schema)
	return schema, err
}
func scanPackage(filename string) (map[string][]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > maxLocalPackageBytes {
		return nil, fmt.Errorf("本地包不是限大小的普通文件")
	}
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("包不是 gzip tar: %w", err)
	}
	defer gzipReader.Close()
	archive := tar.NewReader(io.LimitReader(gzipReader, maxLocalPackageBytes+1))
	files := map[string][]byte{}
	var total int64
	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if _, err = packagePath(entry.Name); err != nil {
			return nil, err
		}
		if entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("包仅允许普通文件；拒绝链接、设备或目录条目")
		}
		if _, exists := files[entry.Name]; exists {
			return nil, fmt.Errorf("包内重复路径")
		}
		if len(files) >= 20000 || entry.Size < 0 || entry.Size > maxLocalPackageBytes-total {
			return nil, fmt.Errorf("包超出文件数/解压大小限制")
		}
		data, err := io.ReadAll(io.LimitReader(archive, entry.Size+1))
		if err != nil || int64(len(data)) != entry.Size {
			return nil, fmt.Errorf("包文件读取不完整")
		}
		total += entry.Size
		files[entry.Name] = data
	}
	if _, err = io.Copy(io.Discard, io.LimitReader(gzipReader, maxLocalPackageBytes-total+1)); err != nil {
		return nil, err
	}
	return files, nil
}
func validateLocalPackage(filename, expectedSHA, goos, arch string, schema int) (localPackageManifest, map[string][]byte, error) {
	var manifest localPackageManifest
	if !validSHA(expectedSHA) {
		return manifest, nil, fmt.Errorf("prepare 必须提供由可信渠道获得的 --sha256")
	}
	actualSHA, err := fileSHA(filename)
	if err != nil {
		return manifest, nil, err
	}
	if actualSHA != expectedSHA {
		return manifest, nil, fmt.Errorf("包 SHA-256 不匹配；未停止实例或暂存文件")
	}
	files, err := scanPackage(filename)
	if err != nil {
		return manifest, nil, err
	}
	data, exists := files["manifest.json"]
	if !exists || len(data) > 1<<20 {
		return manifest, nil, fmt.Errorf("包缺少受限 manifest.json")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return manifest, nil, fmt.Errorf("manifest 无效: %w", err)
	}
	if manifest.Format != "cove-local-package-v1" || !manifest.ManualOnly || manifest.OS != goos || manifest.Arch != arch || !manifest.WebEmbedded || manifest.AdapterVersion == "" || manifest.GoVersion == "" || manifest.NodeVersion == "" || manifest.Version == "" || !validSHA(manifest.SourceID) || manifest.BuildID != manifest.SourceID {
		return manifest, nil, fmt.Errorf("manifest 平台/build/evidence 不符；仅支持当前平台手动可信包")
	}
	if manifest.DataSchema != platformDataSchema || manifest.MinDataSchema != 0 || manifest.MaxDataSchema != platformDataSchema || schema != 0 && schema != platformDataSchema {
		return manifest, nil, fmt.Errorf("数据 schema v%d 不受此包支持；不会迁移或假回滚", schema)
	}
	if len(files) != len(manifest.Files)+1 {
		return manifest, nil, fmt.Errorf("manifest 文件闭包不完整")
	}
	for name, meta := range manifest.Files {
		data, ok := files[name]
		if !ok || !validSHA(meta.SHA256) || meta.Size != int64(len(data)) {
			return manifest, nil, fmt.Errorf("manifest 文件缺失或大小错误: %s", name)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != meta.SHA256 {
			return manifest, nil, fmt.Errorf("manifest 文件 hash 错误: %s", name)
		}
	}
	var evidence struct {
		SourceID  string            `json:"source_id"`
		Sources   map[string]string `json:"source_sha256"`
		Artifacts map[string]string `json:"artifact_sha256"`
	}
	if err = json.Unmarshal(files["bin/build-evidence.json"], &evidence); err != nil || evidence.SourceID != manifest.SourceID {
		return manifest, nil, fmt.Errorf("缺少匹配的 build-evidence")
	}
	sourceJSON, err := json.Marshal(evidence.Sources)
	if err != nil {
		return manifest, nil, err
	}
	sourceHash := sha256.Sum256(sourceJSON)
	if hex.EncodeToString(sourceHash[:]) != manifest.SourceID {
		return manifest, nil, fmt.Errorf("source_id 与 source_sha256 不匹配")
	}
	binary := "bin/gatt"
	if goos == "windows" {
		binary += ".exe"
	}
	required := []string{binary, "bin/source-snapshot.tar.gz"}
	for _, name := range required {
		meta, ok := manifest.Files[name]
		if !ok || evidence.Artifacts[name] != meta.SHA256 {
			return manifest, nil, fmt.Errorf("build-evidence 缺少 %s", name)
		}
	}
	for name, hash := range evidence.Artifacts {
		if meta, ok := manifest.Files[name]; !ok || hash != meta.SHA256 {
			return manifest, nil, fmt.Errorf("artifact evidence 缺少对应文件: %s", name)
		}
	}
	snapshot, err := gzip.NewReader(strings.NewReader(string(files["bin/source-snapshot.tar.gz"])))
	if err != nil {
		return manifest, nil, fmt.Errorf("源码快照无效")
	}
	defer snapshot.Close()
	reader := tar.NewReader(io.LimitReader(snapshot, maxLocalPackageBytes+1))
	seen := map[string]bool{}
	var snapshotSize int64
	for {
		entry, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest, nil, err
		}
		if _, err = packagePath(entry.Name); err != nil {
			return manifest, nil, err
		}
		expected, ok := evidence.Sources[entry.Name]
		if !ok || seen[entry.Name] || entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA || entry.Size < 0 || entry.Size > maxLocalPackageBytes-snapshotSize {
			return manifest, nil, fmt.Errorf("源码快照闭包/路径不符")
		}
		h := sha256.New()
		n, err := io.Copy(h, reader)
		if err != nil || n != entry.Size || hex.EncodeToString(h.Sum(nil)) != expected {
			return manifest, nil, fmt.Errorf("源码快照内容 hash 不符")
		}
		snapshotSize += n
		seen[entry.Name] = true
	}
	if len(seen) != len(evidence.Sources) {
		return manifest, nil, fmt.Errorf("源码快照缺文件")
	}
	return manifest, files, nil
}
func copyPlatformFile(from, to string) error {
	input, err := os.Open(from)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(to), ".cove-copy-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0700); err == nil {
		_, err = io.Copy(temporary, input)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replacePlatformFile(name, to)
}
func (e platformEnvironment) prepareUpdate(filename, expectedSHA string) (string, error) {
	schema, err := readDataSchema(e.DataDir)
	if err != nil {
		return "", err
	}
	manifest, files, err := validateLocalPackage(filename, expectedSHA, e.OS, e.Arch, schema)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(e.Executable)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("目标可执行文件必须是普通文件")
	}
	oldSHA, err := fileSHA(e.Executable)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(e.Executable), ".cove-update-*")
	if err != nil {
		return "", fmt.Errorf("目标目录不可暂存，未停止实例: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(stage)
		}
	}()
	if err = protectPlatformPath(stage, 0700); err != nil {
		return "", err
	}
	for name, data := range files {
		if name == "manifest.json" {
			continue
		}
		relative, _ := packagePath(name)
		if err = writePrivate(filepath.Join(stage, relative), data); err != nil {
			return "", err
		}
	}
	binary := "bin/gatt"
	if e.OS == "windows" {
		binary += ".exe"
	}
	newBinary := filepath.Join(stage, filepath.FromSlash(binary))
	if err = os.Chmod(newBinary, 0700); err != nil {
		return "", err
	}
	updater := filepath.Join(stage, "gatt-updater")
	if e.OS == "windows" {
		updater += ".exe"
	}
	if err = copyPlatformFile(e.Executable, updater); err != nil {
		return "", err
	}
	journal := updateJournal{Format: "cove-update-journal-v1", Phase: "prepared", PreparedAt: time.Now().UTC(), Target: e.Executable, ConfigPath: e.ConfigPath, DataDir: e.DataDir, Listen: e.Listen, OldBinarySHA: oldSHA, NewBinarySHA: manifest.Files[binary].SHA256, OldBuildID: appBuildID(), NewBuildID: manifest.BuildID, OldSchema: schema, DataSchema: manifest.DataSchema, PackageSHA: expectedSHA, Stage: stage, Backup: filepath.Join(stage, "old-binary"), NewBinary: newBinary, Updater: updater, Notice: "仅手动可信 hash 更新，未停止旧实例。apply 将排空、保存一致快照、关闭准入启动并核验新版 readiness，通过后才激活；没有正式发布签名。"}
	journalPath := filepath.Join(stage, "journal.json")
	if err = writePrivate(journalPath, mustPlatformJSON(journal)); err != nil {
		return "", err
	}
	success = true
	return journalPath, nil
}
func readUpdateJournal(filename string, e platformEnvironment) (updateJournal, error) {
	var journal updateJournal
	info, err := os.Lstat(filename)
	if err != nil {
		return journal, err
	}
	if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return journal, fmt.Errorf("更新 journal 必须是私有普通文件")
	}
	file, err := os.Open(filename)
	if err != nil {
		return journal, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 65536))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&journal); err != nil {
		return journal, err
	}
	binary := "gatt"
	if e.OS == "windows" {
		binary += ".exe"
	}
	if journal.Format != "cove-update-journal-v1" || journal.ConfigPath != e.ConfigPath || journal.DataDir != e.DataDir || journal.Listen != e.Listen || !filepath.IsAbs(journal.Target) || filepath.Dir(journal.Stage) != filepath.Dir(journal.Target) || filepath.Join(journal.Stage, "journal.json") != filename || journal.Backup != filepath.Join(journal.Stage, "old-binary") || journal.NewBinary != filepath.Join(journal.Stage, "bin", binary) || !validSHA(journal.OldBinarySHA) || !validSHA(journal.NewBinarySHA) {
		return journal, fmt.Errorf("journal 路径/配置/数据目录不符")
	}
	stageInfo, err := os.Lstat(journal.Stage)
	if err != nil || !stageInfo.IsDir() || stageInfo.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && stageInfo.Mode().Perm()&0077 != 0 {
		return journal, fmt.Errorf("暂存目录不是私有真实目录")
	}
	return journal, nil
}
func withStoppedUpdate(e platformEnvironment, journal updateJournal, action func() error) error {
	lock, err := lockDataDir(e.DataDir)
	if err != nil {
		return fmt.Errorf("实例仍占用数据目录；先 service stop 或原窗口正常退出: %w", err)
	}
	defer lock.Close()
	listener, err := net.Listen("tcp", e.Listen)
	if err != nil {
		return fmt.Errorf("监听端口仍占用，拒绝替换: %w", err)
	}
	defer listener.Close()
	schema, err := readDataSchema(e.DataDir)
	if err != nil {
		return err
	}
	if schema != journal.OldSchema {
		return fmt.Errorf("数据 schema 已改变；必须旧 binary 与更新前数据快照共同恢复，不能假回滚")
	}
	return action()
}
func (e platformEnvironment) applyUpdate(filename string, rollback bool) error {
	journal, err := readUpdateJournal(filename, e)
	if err != nil {
		return err
	}
	if e.OS == "windows" && e.Executable == journal.Target {
		return fmt.Errorf("Windows 不能由正在运行的目标 exe 替换自身；请运行 journal.updater")
	}
	return withStoppedUpdate(e, journal, func() error {
		currentSHA, err := fileSHA(journal.Target)
		if err != nil {
			return err
		}
		if rollback {
			if journal.Phase == "rolled_back" && currentSHA == journal.OldBinarySHA {
				return nil
			}
			if journal.Phase != "replaced_pending_start" {
				return fmt.Errorf("此阶段不允许 binary 回退")
			}
			if record, err := readRuntime(e.DataDir); err == nil && record.StartedAt.After(journal.PreparedAt) {
				return fmt.Errorf("准备后有实例启动；数据可能已有新请求，拒绝自动回退。请另做恢复预览")
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			backupSHA, err := fileSHA(journal.Backup)
			if err != nil || backupSHA != journal.OldBinarySHA || currentSHA != journal.NewBinarySHA {
				return fmt.Errorf("回退 backup/target hash 不符")
			}
			if err = copyPlatformFile(journal.Backup, journal.Target); err != nil {
				return err
			}
			journal.Phase = "rolled_back"
			journal.Notice = "已恢复旧 binary，数据目录原样保留；未恢复/迁移数据或启动服务。"
			return writePrivate(filename, mustPlatformJSON(journal))
		}
		if journal.Phase == "replaced_pending_start" && currentSHA == journal.NewBinarySHA {
			return nil
		}
		if journal.Phase != "prepared" && journal.Phase != "backed_up" {
			return fmt.Errorf("此 journal 阶段不允许 apply")
		}
		newSHA, err := fileSHA(journal.NewBinary)
		if err != nil || newSHA != journal.NewBinarySHA {
			return fmt.Errorf("暂存 binary hash 不符")
		}
		if journal.Phase == "backed_up" {
			backupSHA, backupErr := fileSHA(journal.Backup)
			if backupErr != nil || backupSHA != journal.OldBinarySHA {
				return fmt.Errorf("旧 binary backup 无效")
			}
		}
		if journal.Phase == "backed_up" && currentSHA == journal.NewBinarySHA {
			journal.Phase = "replaced_pending_start"
			journal.Notice = "检测到已完成替换；仍等待用户启动并验证。"
			return writePrivate(filename, mustPlatformJSON(journal))
		}
		if currentSHA != journal.OldBinarySHA {
			return fmt.Errorf("当前 binary 自 prepare 后已改变，拒绝覆盖")
		}
		if journal.Phase == "prepared" {
			if err = copyPlatformFile(journal.Target, journal.Backup); err != nil {
				return err
			}
			journal.Phase = "backed_up"
			if err = writePrivate(filename, mustPlatformJSON(journal)); err != nil {
				return err
			}
		} else {
			backupSHA, err := fileSHA(journal.Backup)
			if err != nil || backupSHA != journal.OldBinarySHA {
				return fmt.Errorf("旧 binary backup 无效")
			}
		}
		if err = copyPlatformFile(journal.NewBinary, journal.Target); err != nil {
			return fmt.Errorf("binary 替换失败，旧 binary backup 保留，journal=backed_up: %w", err)
		}
		journal.Phase = "replaced_pending_start"
		journal.Notice = "binary 已替换，尚未启动/验证。请手动 service start，再 status 核对新 build。自动 readiness/数据回退未接线。"
		return writePrivate(filename, mustPlatformJSON(journal))
	})
}
func (e platformEnvironment) updateCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法：update prepare --package FILE --sha256 TRUSTED_HASH | apply/rollback --journal FILE")
	}
	flags := flag.NewFlagSet("update "+args[0], flag.ContinueOnError)
	flags.SetOutput(e.Output)
	filename := flags.String("package", "", "本地 tar.gz 包")
	hash := flags.String("sha256", "", "可信包 SHA-256")
	journal := flags.String("journal", "", "准备阶段 journal 的绝对路径")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("update 不接受额外参数")
	}
	switch args[0] {
	case "prepare":
		if *filename == "" {
			return fmt.Errorf("缺少 --package")
		}
		journalPath, err := e.prepareUpdate(*filename, *hash)
		if err != nil {
			return err
		}
		record, err := readUpdateJournal(journalPath, e)
		if err != nil {
			return err
		}
		_, err = e.Output.Write(mustPlatformJSON(struct {
			Journal string        `json:"journal"`
			Update  updateJournal `json:"update"`
		}{journalPath, record}))
		return err
	case "apply", "rollback":
		if *journal == "" {
			return fmt.Errorf("缺少 --journal")
		}
		var err error
		if args[0] == "apply" {
			err = e.applyUpdateSwitch(ctx, *journal, nativePlatformRestoreSpawn)
		} else {
			err = e.applyUpdate(*journal, true)
		}
		if err != nil {
			return err
		}
		record, err := readUpdateJournal(*journal, e)
		if err != nil {
			return err
		}
		_, err = e.Output.Write(mustPlatformJSON(record))
		return err
	default:
		return fmt.Errorf("不支持此 update 动作；使用 prepare、apply 或停止准入前的 rollback")
	}
}
