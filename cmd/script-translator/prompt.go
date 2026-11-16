package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go-script-translate/internal/config"
	"go-script-translate/internal/prompt"
)

func newPromptCmd(deps cliDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prompt",
		Short: "Inspect prompt templates, default example JSON files, and rendered system prompts",
	}
	cmd.AddCommand(newPromptKindCmd(
		"template",
		"Print the raw .gotmpl template for entity_recognition or translation",
		prompt.RawTemplate,
		deps,
	))
	cmd.AddCommand(newPromptKindCmd(
		"example",
		"Print the built-in default example JSON for entity_recognition or translation",
		prompt.DefaultExampleJSON,
		deps,
	))
	cmd.AddCommand(newPromptRenderCmd(
		prompt.KindEntityRecognition,
		"Render the final entity recognition system prompt",
		deps,
	))
	cmd.AddCommand(newPromptRenderCmd(
		prompt.KindTranslation,
		"Render the final translation system prompt",
		deps,
	))
	return cmd
}

func newPromptKindCmd(
	use string,
	short string,
	fetch func(string) (string, error),
	deps cliDeps,
) *cobra.Command {
	return &cobra.Command{
		Use:       fmt.Sprintf("%s <entity_recognition|translation>", use),
		Short:     short,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{prompt.KindEntityRecognition, prompt.KindTranslation},
		RunE: func(_ *cobra.Command, args []string) error {
			out, err := fetch(strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}
			return writeLine(deps, out)
		},
	}
}

func newPromptRenderCmd(kind, short string, deps cliDeps) *cobra.Command {
	var (
		cfgPath    string
		useDefault bool
	)

	cmd := &cobra.Command{
		Use:   kind,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			lang := prompt.DefaultLanguage()
			var cfg config.Config
			if !useDefault {
				resolved, err := resolveConfigPath(cfgPath, deps)
				if err != nil {
					return err
				}
				cfg, err = config.Load(resolved)
				if err != nil {
					return err
				}
				lang = cfg.Language
			}

			var (
				rendered string
				err      error
			)
			switch kind {
			case prompt.KindEntityRecognition:
				rendered, err = prompt.RenderEntityRecognition(lang, cfg.EntityRecognition.Example)
			case prompt.KindTranslation:
				rendered, err = prompt.RenderTranslation(lang, cfg.Translation.Example)
			default:
				return fmt.Errorf("prompt: unknown kind %q", kind)
			}
			if err != nil {
				return err
			}
			return writeLine(deps, rendered)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&cfgPath, "config", "c", "", "Path to JSON config file (defaults to user config path)")
	flags.BoolVar(&useDefault, "default", false, "Render using built-in defaults without reading config.json")
	return cmd
}

func writeLine(deps cliDeps, s string) error {
	if strings.HasSuffix(s, "\n") {
		_, err := fmt.Fprint(deps.stdout, s)
		return err
	}
	_, err := fmt.Fprintln(deps.stdout, s)
	return err
}
