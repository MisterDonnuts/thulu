package initwizard

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
)

const AnswersFile = "deploy/.octopus-init.json"

// Secrets are kept out of this file only by `json:"-"` tags on Config; every new secret field needs
// one.
func SaveAnswers(root string, cfg *Config) error {
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return apperr.Wrap(err, "initwizard.errors.encode_answers")
	}
	if err := writePrivate(filepath.Join(root, AnswersFile), append(body, '\n')); err != nil {
		return apperr.Wrap(err, "initwizard.errors.write_file", "path", AnswersFile)
	}
	return nil
}

func LoadAnswers(root string) (*Config, error) {
	//nolint:gosec // reads this install's own answers file, under the repo root the operator named
	raw, err := os.ReadFile(filepath.Join(root, AnswersFile))
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, apperr.Wrap(err, "initwizard.errors.read_file", "path", AnswersFile)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, apperr.Wrap(err, "initwizard.errors.parse_file", "path", AnswersFile)
	}
	return &cfg, nil
}

// Not os.WriteFile: a truncate-then-crash leaves .env without its keys and the next run mints new
// ones for a database holding the old,
// and os.WriteFile keeps an existing 0644 mode.
func writePrivate(path string, body []byte) error {
	return fsx.WriteFile(path, body, 0o600)
}
