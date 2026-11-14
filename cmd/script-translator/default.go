package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"go-script-translate/internal/config"
	"go-script-translate/internal/observation"
)

const defaultTemplateModel = "gpt-4.1-mini"

type defaultEntry struct {
	Key   string
	Value string
}

type defaultReport struct {
	Config     string `json:"CONFIG"`
	Input      string `json:"INPUT"`
	Output     string `json:"OUTPUT"`
	CacheDir   string `json:"CACHE_DIR"`
	APIKeyEnv  string `json:"APIKEY_ENV"`
	BatchSize  int    `json:"BATCH_SIZE"`
	MaxAttempt int    `json:"MAX_ATTEMPT"`
	WaitTime   int    `json:"WAIT_TIME"`
	RetryTime  int    `json:"RETRY_TIME"`
	LogLevel   string `json:"LOG_LEVEL"`
}

type minimalModelConfig struct {
	Model string `json:"model"`
}

type minimalConfigTemplate struct {
	EntityRecognition minimalModelConfig `json:"entity_recognition"`
	Translation       minimalModelConfig `json:"translation"`
}

func newDefaultCmd(deps cliDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "default",
		Short: "Inspect or initialize default paths and configuration values",
	}
	cmd.AddCommand(newDefaultAllCmd(deps))
	cmd.AddCommand(newDefaultWhichCmd(deps))
	cmd.AddCommand(newDefaultInitCmd(deps))
	return cmd
}

func newDefaultAllCmd(deps cliDeps) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "all",
		Short: "List all default settings and paths",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			entries, err := resolveDefaultEntries(deps)
			if err != nil {
				return err
			}

			if asJSON {
				cfgPath := ""
				for _, entry := range entries {
					if entry.Key == "CONFIG" {
						cfgPath = entry.Value
						break
					}
				}
				report := defaultReport{
					Config:     cfgPath,
					Input:      defaultInputPath,
					Output:     defaultOutputPath,
					CacheDir:   config.DefaultCacheDir,
					APIKeyEnv:  config.DefaultAPIKeyEnv,
					BatchSize:  config.DefaultBatchSize,
					MaxAttempt: config.DefaultMaxAttempt,
					WaitTime:   config.DefaultWaitTime,
					RetryTime:  config.DefaultRetryTime,
					LogLevel:   observation.DefaultLogLevel,
				}
				data, err := marshalIndentedLine(report)
				if err != nil {
					return fmt.Errorf("encode defaults JSON: %w", err)
				}
				_, err = deps.stdout.Write(data)
				return err
			}

			tw := tabwriter.NewWriter(deps.stdout, 0, 0, 2, ' ', 0)
			for _, entry := range entries {
				if _, err := fmt.Fprintf(tw, "%s\t%s\n", entry.Key, entry.Value); err != nil {
					return err
				}
			}
			return tw.Flush()
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output defaults as JSON")
	return cmd
}

func newDefaultWhichCmd(deps cliDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "which <key>",
		Short: "Print a single default setting or path by key",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			entries, err := resolveDefaultEntries(deps)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			prefix := normalizeDefaultKey(toComplete)
			var matches []string
			for _, entry := range entries {
				if strings.HasPrefix(entry.Key, prefix) {
					matches = append(matches, entry.Key)
				}
			}
			return matches, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error {
			entries, err := resolveDefaultEntries(deps)
			if err != nil {
				return err
			}

			normalized := normalizeDefaultKey(args[0])
			if normalized == "API_KEY_ENV" {
				normalized = "APIKEY_ENV"
			}
			for _, entry := range entries {
				if entry.Key == normalized {
					_, err := fmt.Fprintf(deps.stdout, "%s\n", entry.Value)
					return err
				}
			}

			keys := make([]string, len(entries))
			for i, entry := range entries {
				keys[i] = entry.Key
			}
			return fmt.Errorf("unknown default key %q (valid keys: %s)", args[0], strings.Join(keys, ", "))
		},
	}
}

func newDefaultInitCmd(deps cliDeps) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a minimal config.json at the default user config location",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfgPath, err := resolveConfigPath("", deps)
			if err != nil {
				return err
			}

			if _, err := os.Stat(cfgPath); err == nil {
				if !force {
					return fmt.Errorf("config already exists at %s (pass --force to overwrite)", cfgPath)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("check config path %q: %w", cfgPath, err)
			}

			if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
				return fmt.Errorf("create config directory: %w", err)
			}

			tmpl := minimalConfigTemplate{
				EntityRecognition: minimalModelConfig{Model: defaultTemplateModel},
				Translation:       minimalModelConfig{Model: defaultTemplateModel},
			}
			data, err := marshalIndentedLine(tmpl)
			if err != nil {
				return fmt.Errorf("encode minimal config JSON: %w", err)
			}
			if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
				return fmt.Errorf("write config %q: %w", cfgPath, err)
			}

			_, err = fmt.Fprintf(deps.stdout, "wrote minimal config to %s\n", cfgPath)
			return err
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing config file at the default path")
	return cmd
}

func resolveDefaultEntries(deps cliDeps) ([]defaultEntry, error) {
	cfgPath, err := resolveConfigPath("", deps)
	if err != nil {
		return nil, err
	}

	return []defaultEntry{
		{Key: "CONFIG", Value: cfgPath},
		{Key: "INPUT", Value: defaultInputPath},
		{Key: "OUTPUT", Value: defaultOutputPath},
		{Key: "CACHE_DIR", Value: config.DefaultCacheDir},
		{Key: "APIKEY_ENV", Value: config.DefaultAPIKeyEnv},
		{Key: "BATCH_SIZE", Value: strconv.Itoa(config.DefaultBatchSize)},
		{Key: "MAX_ATTEMPT", Value: strconv.Itoa(config.DefaultMaxAttempt)},
		{Key: "WAIT_TIME", Value: strconv.Itoa(config.DefaultWaitTime)},
		{Key: "RETRY_TIME", Value: strconv.Itoa(config.DefaultRetryTime)},
		{Key: "LOG_LEVEL", Value: observation.DefaultLogLevel},
	}, nil
}

func normalizeDefaultKey(raw string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(raw), "-", "_"))
}
