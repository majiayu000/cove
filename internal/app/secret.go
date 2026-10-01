package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
)

type Secrets interface {
	Health() error
	Get(string) (string, error)
	Put(string, string) error
	Delete(string) error
}

// FileSecrets owns the credential file for one running Gatt instance.
// Values are published to the in-memory cache only after an atomic file replacement.
type FileSecrets struct {
	mu     sync.Mutex
	path   string
	values map[string]string
	fault  error
}

func NewFileSecrets(dataDir string) (*FileSecrets, error) {
	dir := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("凭据目录创建失败: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("凭据目录必须为普通目录")
	}
	if err := protectAppPath(dir, 0700); err != nil {
		return nil, fmt.Errorf("凭据目录权限设置失败: %w", err)
	}
	v := &FileSecrets{path: filepath.Join(dir, "credentials.json"), values: map[string]string{}}
	info, err = os.Lstat(v.path)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return nil, fmt.Errorf("凭据文件检查失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("凭据文件必须为普通文件")
	}
	if err := protectAppPath(v.path, 0600); err != nil {
		return nil, fmt.Errorf("凭据文件权限设置失败: %w", err)
	}
	b, err := os.ReadFile(v.path)
	if err != nil {
		return nil, fmt.Errorf("凭据文件读取失败: %w", err)
	}
	if json.Unmarshal(b, &v.values) != nil || v.values == nil {
		return nil, errors.New("凭据文件内容损坏；请从备份恢复，不会自动覆盖")
	}
	return v, nil
}

// Health reports the existing persistence fault latch without reading secrets.
func (v *FileSecrets) Health() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.fault
}

func (v *FileSecrets) Get(ref string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fault != nil {
		return "", v.fault
	}
	value, ok := v.values[ref]
	if !ok {
		return "", fmt.Errorf("本地凭据缺失，请重新配置或登录: %w", os.ErrNotExist)
	}
	return value, nil
}

func (v *FileSecrets) Put(ref, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fault != nil {
		return v.fault
	}
	if _, exists := v.values[ref]; exists {
		return fmt.Errorf("凭据项已存在，未覆盖: %w", os.ErrExist)
	}
	next := maps.Clone(v.values)
	next[ref] = value
	return v.save(next)
}

func (v *FileSecrets) Delete(ref string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fault != nil {
		return v.fault
	}
	if _, exists := v.values[ref]; !exists {
		return nil
	}
	next := maps.Clone(v.values)
	delete(next, ref)
	return v.save(next)
}

func (v *FileSecrets) save(next map[string]string) (result error) {
	b, err := json.Marshal(next)
	if err != nil {
		return errors.New("凭据编码失败")
	}
	f, err := os.CreateTemp(filepath.Dir(v.path), ".credentials-*") // mode 0600
	if err != nil {
		return fmt.Errorf("凭据临时文件创建失败: %w", err)
	}
	defer func() {
		if err := os.Remove(f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("凭据临时文件清理失败: %w", err))
		}
	}()
	_, writeErr := f.Write(b)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err = errors.Join(writeErr, f.Close()); err != nil {
		return fmt.Errorf("凭据文件保存失败: %w", err)
	}
	if err = replaceAppFile(f.Name(), v.path); err != nil {
		return fmt.Errorf("凭据文件替换失败: %w", err)
	}
	dir, err := os.Open(filepath.Dir(v.path))
	if err == nil {
		err = errors.Join(syncAppDirectory(dir), dir.Close())
	}
	if err != nil {
		// Rename happened, but its durability is uncertain. Neither the old cache
		// nor the new value may be used until a restart reloads persisted state.
		v.fault = errors.New("凭据持久化结果不确定，已暂停凭据读写；请重启后检查账号状态")
		return v.fault
	}
	v.values = next
	return nil
}

// RecoverAdmin is only called with the service stopped and the data-directory
// lock held. A failure is reported explicitly; recovery can then be retried.
func RecoverAdmin(store *Store, vault *FileSecrets) error {
	secret := token()
	tx, err := store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO settings(key,value) VALUES('admin_digest',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", digest(secret)); err != nil {
		return err
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if vault.fault != nil {
		return vault.fault
	}
	next := maps.Clone(vault.values)
	next["administrator"] = secret
	if err = vault.save(next); err != nil {
		return err
	}
	return tx.Commit()
}
