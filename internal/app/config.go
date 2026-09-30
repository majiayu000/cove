package app

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

const Version = "0.1.0-dev"

// Set by the build target to the source snapshot SHA-256.
var BuildID = "development"

type CodexConfig struct {
	BaseURL       string `json:"base_url"`
	AuthBaseURL   string `json:"auth_base_url"`
	ClientID      string `json:"client_id"`
	ClientVersion string `json:"client_version"`
	RedirectURI   string `json:"redirect_uri"`
}
type Config struct {
	Listen        string      `json:"listen"`
	DataDir       string      `json:"data_dir"`
	MaxBody       int64       `json:"max_body_bytes"`
	MaxResponse   int64       `json:"max_response_bytes"`
	MaxEvent      int         `json:"max_event_bytes"`
	MaxConcurrent int         `json:"max_concurrent"`
	HeaderTimeout int         `json:"header_timeout_seconds"`
	IdleTimeout   int         `json:"idle_timeout_seconds"`
	TotalTimeout  int         `json:"total_timeout_seconds"`
	RetentionDays int         `json:"retention_days"`
	Codex         CodexConfig `json:"codex"`
}

func LoadConfig(path string) (Config, error) { return LoadConfigWithDataDir(path, "") }
func LoadConfigWithDataDir(path, selectedDir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, errors.New("配置 JSON 无效")
	}
	if selectedDir != "" {
		c.DataDir = selectedDir
	}
	if c.DataDir == "" {
		c.DataDir, err = DefaultDataDir()
		if err != nil {
			return c, err
		}
	}
	if err = loadSavedRuntimeConfig(&c); err != nil {
		return c, err
	}
	host, port, err := net.SplitHostPort(c.Listen)
	p, e := strconv.Atoi(port)
	if err != nil || e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || p < 1 || p > 65535 {
		return c, errors.New("listen 必须是 loopback IP 和固定有效端口")
	}
	if c.MaxBody < 1 || c.MaxResponse < 1 || c.MaxEvent < 1 || c.MaxConcurrent < 1 || c.HeaderTimeout < 1 || c.IdleTimeout < 1 || c.TotalTimeout < 1 || c.RetentionDays < 1 {
		return c, errors.New("请求限制、超时和保留期必须为正数")
	}
	if err := validateURL(c.Codex.BaseURL); err != nil {
		return c, err
	}
	if err := validateURL(c.Codex.AuthBaseURL); err != nil {
		return c, err
	}
	return c, nil
}

func DefaultDataDir() (string, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cove"), nil
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("LOCALAPPDATA 未设置")
		}
		return filepath.Join(base, "Cove"), nil
	default:
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "Cove"), nil
	}
}
