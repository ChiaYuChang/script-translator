package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"go-script-translate/internal/entitystore"
	"go-script-translate/internal/model"
	"go-script-translate/internal/translate"
)

func newPipelineCmd(deps cliDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Run script entity recognition and translation stages",
	}
	cmd.AddCommand(newPipelineRecognizeCmd(deps))
	cmd.AddCommand(newPipelineTranslateCmd(deps))
	return cmd
}

func newPipelineRecognizeCmd(deps cliDeps) *cobra.Command {
	var (
		configPath string
		inPath     string
		dictPath   string
		outPath    string
		saveRawDir string
	)

	cmd := &cobra.Command{
		Use:   "recognize",
		Short: "Extract script dialogue and recognize named entities for review or translation",
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			inPath = strings.TrimSpace(inPath)
			if inPath == "" {
				return errors.New("missing input script path (--in)")
			}

			cfg, obs, provider, err := initObserverAndProvider(configPath, saveRawDir, "recognize", deps)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := obs.Close(); cerr != nil {
					runErr = errors.Join(runErr, cerr)
				}
			}()

			script, err := translate.Read(inPath)
			if err != nil {
				return err
			}
			units, _, err := translate.Extract(script)
			if err != nil {
				return err
			}

			var knownDict []model.Entity
			if d := strings.TrimSpace(dictPath); d != "" {
				mapping, err := entitystore.Load(d)
				if err != nil {
					return err
				}
				knownDict = mapping.Entities
			}

			params := cfg.EntityRecognition
			params.Dictionary = knownDict

			transcript := model.TranslateRequest{
				Entity:   knownDict,
				Dialogue: units,
			}.Transcript()

			obs.Logger.InfoContext(cmd.Context(), "starting entity recognition",
				slog.String("input", inPath),
				slog.Int("dialogue_units", len(units)),
				slog.Int("dictionary_entities", len(knownDict)),
			)

			recognized, err := provider.RecognizeEntities(cmd.Context(), transcript, params)
			if err != nil {
				return err
			}
			if recognized == nil {
				recognized = []model.Entity{}
			}

			outBytes, err := marshalIndentedLine(recognizePayload{
				Input:    inPath,
				Entities: recognized,
			})
			if err != nil {
				return fmt.Errorf("encode recognize output: %w", err)
			}

			obs.Logger.InfoContext(cmd.Context(), "entity recognition completed",
				slog.Int("entities", len(recognized)),
			)
			return writeOutputBytes(outPath, outBytes, deps.stdout)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&configPath, "config", "c", defaultConfigPath, "Path to config JSON file")
	flags.StringVarP(&inPath, "in", "i", "", "Path to source script file")
	flags.StringVarP(&dictPath, "dict", "d", "", "Optional path to entity_dict JSON file")
	flags.StringVarP(&outPath, "out", "o", "-", "Output JSON path ('-' writes to stdout)")
	flags.StringVar(&saveRawDir, "save-raw", "", "Optional directory to save raw Responses API JSON bodies as fixtures")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

func newPipelineTranslateCmd(deps cliDeps) *cobra.Command {
	var (
		configPath   string
		inPath       string
		dictPath     string
		entitiesPath string
		outPath      string
		saveRawDir   string
	)

	cmd := &cobra.Command{
		Use:   "translate",
		Short: "Translate script dialogue using recognized entities or an entity dictionary",
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			inPath = strings.TrimSpace(inPath)
			dictPath = strings.TrimSpace(dictPath)
			entitiesPath = strings.TrimSpace(entitiesPath)

			if cmd.Flags().Changed("entities") && cmd.Flags().Changed("dict") {
				return errors.New("cannot combine --dict with --entities")
			}

			var entities []model.Entity
			var err error
			switch {
			case cmd.Flags().Changed("entities"):
				entities, err = loadRecognizeEntities(entitiesPath, deps.stdin, &inPath)
				if err != nil {
					return err
				}
			case dictPath != "":
				mapping, err := entitystore.Load(dictPath)
				if err != nil {
					return err
				}
				entities = mapping.Entities
			case deps.stdinIsTTY != nil && !deps.stdinIsTTY():
				entities, err = loadRecognizeEntities("-", deps.stdin, &inPath)
				if err != nil {
					return err
				}
			}

			if inPath == "" {
				return errors.New("missing input script path: specify --in or pipe output from pipeline recognize")
			}

			cfg, obs, provider, err := initObserverAndProvider(configPath, saveRawDir, "translate", deps)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := obs.Close(); cerr != nil {
					runErr = errors.Join(runErr, cerr)
				}
			}()

			script, err := translate.Read(inPath)
			if err != nil {
				return err
			}
			units, retained, err := translate.Extract(script)
			if err != nil {
				return err
			}

			obs.Logger.InfoContext(cmd.Context(), "starting translation",
				slog.String("input", inPath),
				slog.Int("dialogue_units", len(units)),
				slog.Int("entities", len(entities)),
			)

			resp, err := provider.Translate(cmd.Context(), model.TranslateRequest{
				Entity:   entities,
				Dialogue: units,
			}, cfg.Translation)
			if err != nil {
				return err
			}

			assembled, err := translate.Assemble(units, resp, retained, entities)
			if err != nil {
				return err
			}

			obs.Logger.InfoContext(cmd.Context(), "translation completed",
				slog.String("input", inPath),
				slog.Int("dialogue_units", len(resp)),
			)
			return writeOutputBytes(outPath, []byte(assembled), deps.stdout)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&configPath, "config", "c", defaultConfigPath, "Path to config JSON file")
	flags.StringVarP(&inPath, "in", "i", "", "Path to source script file (optional when piped from recognize)")
	flags.StringVarP(&dictPath, "dict", "d", "", "Optional path to entity_dict JSON file when not reading recognize output")
	flags.StringVarP(&entitiesPath, "entities", "e", "-", "Path to recognize output JSON ('-' reads stdin when piped)")
	flags.StringVarP(&outPath, "out", "o", "-", "Output translated script path ('-' writes to stdout)")
	flags.StringVar(&saveRawDir, "save-raw", "", "Optional directory to save raw Responses API JSON bodies as fixtures")
	return cmd
}

func loadRecognizeEntities(path string, stdin io.Reader, inPath *string) ([]model.Entity, error) {
	raw, err := readInputBytes(path, stdin)
	if err != nil {
		return nil, err
	}
	payload, err := decodeRecognizePayload(raw)
	if err != nil {
		return nil, err
	}
	if *inPath == "" {
		*inPath = strings.TrimSpace(payload.Input)
	}
	return payload.Entities, nil
}
