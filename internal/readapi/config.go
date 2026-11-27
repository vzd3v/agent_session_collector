package readapi

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Catalog      string        `toml:"catalog"`
	Mirror       string        `toml:"mirror"`
	Archive      string        `toml:"archive"`
	TokensFile   string        `toml:"tokens_file"`
	Listen       string        `toml:"listen"`
	Socket       string        `toml:"socket"`
	MaxBytes     int64         `toml:"max_bytes"`
	MaxTransfers int           `toml:"max_transfers"`
	Timeout      time.Duration `toml:"timeout"`
}

func LoadConfig(path string) (Config, error) {
	cfg := Config{MaxBytes: 512 << 20, MaxTransfers: 4, Timeout: 2 * time.Minute}
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, err
	}
	if keys := md.Undecoded(); len(keys) != 0 {
		return Config{}, fmt.Errorf("unknown read API setting %s", keys[0])
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	for _, entry := range []struct{ name, path string }{
		{"catalog", c.Catalog}, {"mirror", c.Mirror}, {"archive", c.Archive}, {"tokens_file", c.TokensFile},
	} {
		if !filepath.IsAbs(entry.path) {
			return fmt.Errorf("%s must be an absolute path", entry.name)
		}
	}
	if (c.Listen == "") == (c.Socket == "") {
		return errors.New("configure exactly one of listen or socket")
	}
	if c.Socket != "" && !filepath.IsAbs(c.Socket) {
		return errors.New("socket must be an absolute path")
	}
	if c.Listen != "" {
		host, _, err := net.SplitHostPort(c.Listen)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("listen must use a loopback IP and port")
		}
	}
	if c.MaxBytes < 1 || c.MaxBytes > 16<<30 || c.MaxTransfers < 1 || c.MaxTransfers > 64 || c.Timeout < time.Second {
		return errors.New("invalid transfer limits or timeout")
	}
	return nil
}
