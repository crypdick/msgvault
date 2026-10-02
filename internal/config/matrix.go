package config

import "errors"

// MatrixConfig controls the dedicated account's scheduled chat archive.
// An empty Rooms list discovers all joined chat rooms on each run.
type MatrixConfig struct {
	Enabled  bool     `toml:"enabled"`
	Schedule string   `toml:"schedule"`
	Rooms    []string `toml:"rooms"`
	MaxPages int      `toml:"max_pages"`
}

func (m MatrixConfig) Validate() error {
	if m.MaxPages < 0 || (m.Enabled && m.Schedule != "" && m.MaxPages == 0) {
		return errors.New("matrix.max_pages must be positive for scheduled sync")
	}
	return nil
}
