package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

// Config is the on-disk configuration. Defaults are set in LoadConfig before
// the file is read, so the file needs to contain only the changed fields.
type Config struct {
	SiteName         string   `json:"site_name"`
	SiteURL          string   `json:"site_url"`
	Listen           string   `json:"listen"`
	DBPath           string   `json:"db_path"`
	Terms            string   `json:"terms"`
	Footer           string   `json:"footer"`
	BlockedCountries []string `json:"blocked_countries"`
	TrustedProxies   []string `json:"trusted_proxies"`
	GeoV4File        string   `json:"geo_v4_file"`
	SMTPHost         string   `json:"smtp_host"`
	MailFrom         string   `json:"mail_from"`
	InviteQuota      int      `json:"invite_quota"`
	SessionDays      int      `json:"session_days"`
	PostsPerPage     int      `json:"posts_per_page"`
}

// LoadConfig reads and validates the configuration file.
func LoadConfig(path string) (*Config, error) {
	conf := &Config{
		SiteName:         "Blog",
		Listen:           "127.0.0.1:8080",
		DBPath:           "blog.db",
		BlockedCountries: []string{"GB", "AU"},
		SMTPHost:         "localhost:25",
		InviteQuota:      5,
		SessionDays:      30,
		PostsPerPage:     50,
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(conf); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := conf.Validate(); err != nil {
		return nil, err
	}
	return conf, nil
}

// Validate checks the values that the program cannot repair by itself.
func (conf *Config) Validate() error {
	if conf.SiteURL == "" {
		return errors.New("site_url is required, because the login link uses it")
	}
	conf.SiteURL = strings.TrimRight(conf.SiteURL, "/")
	if !strings.HasPrefix(conf.SiteURL, "http://") &&
		!strings.HasPrefix(conf.SiteURL, "https://") {
		return errors.New("site_url must start with http:// or https://")
	}
	if conf.MailFrom == "" {
		return errors.New("mail_from is required")
	}
	if conf.SessionDays < 1 {
		return errors.New("session_days must be 1 or more")
	}
	if conf.InviteQuota < 0 {
		return errors.New("invite_quota must be 0 or more")
	}
	if conf.PostsPerPage < 1 || conf.PostsPerPage > 200 {
		return errors.New("posts_per_page must be between 1 and 200")
	}

	// The ISO 3166-1 alpha-2 code for the United Kingdom is GB. The code UK
	// is not assigned, so a block list that contains UK blocks nothing.
	for idx, code := range conf.BlockedCountries {
		upper := strings.ToUpper(strings.TrimSpace(code))
		if len(upper) != 2 {
			return fmt.Errorf("blocked_countries[%d]: %q is not a two-letter code", idx, code)
		}
		if upper == "UK" {
			log.Printf("config warning: UK is not an ISO country code, use GB")
		}
		conf.BlockedCountries[idx] = upper
	}
	return nil
}

// Reload reads the file again and replaces the live configuration.
// The listen address and the database path are not reloadable, so this
// function keeps the values from startup.
func (app *App) Reload(path string) error {
	fresh, err := LoadConfig(path)
	if err != nil {
		return err
	}
	live := app.Conf()
	fresh.Listen = live.Listen
	fresh.DBPath = live.DBPath

	app.conf.Store(fresh)

	table, err := LoadGeo(fresh)
	if err != nil {
		return fmt.Errorf("reload geo: %w", err)
	}
	app.geo.Store(table)
	return nil
}

