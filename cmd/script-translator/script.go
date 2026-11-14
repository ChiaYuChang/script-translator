package main

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"go-script-translate/internal/entitystore"
	"go-script-translate/internal/model"
	"go-script-translate/internal/translate"
)

func newScriptCmd(deps cliDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "script",
		Short: "Extract, hash, recognize, translate, assemble, or run script localization stages",
	}
	cmd.AddCommand(newScriptExtractCmd(deps))
	cmd.AddCommand(newScriptHashsumCmd(deps))
	cmd.AddCommand(newScriptRecognizeCmd(deps))
	cmd.AddCommand(newScriptTranslateCmd(deps))
	cmd.AddCommand(newScriptAssembleCmd(deps))
	cmd.AddCommand(newScriptRunCmd(deps))
	return cmd
}

func newScriptExtractCmd(deps cliDeps) *cobra.Command {
	var (
		inPath  string
		outPath string
	)

	cmd := &cobra.Command{
		Use:   "extract [script]",
		Short: "Extract script dialogue units, retained skeleton lines, and transcript hashsum to JSON",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedIn, err := resolveScriptInputArg(cmd, args, inPath)
			if err != nil {
				return err
			}

			raw, err := readInputBytes(resolvedIn, deps.stdin)
			if err != nil {
				return err
			}
			parsed, err := translate.ParseBytes(raw)
			if err != nil {
				return fmt.Errorf("read script %q: %w", resolvedIn, err)
			}

			inputLabel := resolvedIn
			if inputLabel == "-" {
				inputLabel = ""
			}
			extracted, warnings, err := translate.ExtractScriptWithWarnings(inputLabel, parsed)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				if _, werr := fmt.Fprintf(deps.stderr, "warning: %s\n", w); werr != nil {
					return werr
				}
			}

			outBytes, err := marshalIndentedLine(extracted)
			if err != nil {
				return fmt.Errorf("encode extracted script JSON: %w", err)
			}
			return writeOutputBytes(outPath, outBytes, deps.stdout)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&inPath, "in", "i", defaultInputPath, "Path to source script file ('-' reads from stdin)")
	flags.StringVarP(&outPath, "out", "o", "-", "Output extracted JSON path ('-' writes to stdout)")
	return cmd
}

func newScriptHashsumCmd(deps cliDeps) *cobra.Command {
	var inPath string

	cmd := &cobra.Command{
		Use:   "hashsum [script]",
		Short: "Print SHA-256 hashsum of a script's extracted transcript",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedIn, err := resolveScriptInputArg(cmd, args, inPath)
			if err != nil {
				return err
			}
			extracted, err := loadScriptSource(resolvedIn, deps)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(deps.stdout, "%s\n", extracted.Hashsum)
			return err
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&inPath, "in", "i", defaultInputPath, "Path to source script or extracted JSON file ('-' reads from stdin)")
	return cmd
}

func newScriptRecognizeCmd(deps cliDeps) *cobra.Command {
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

			extracted, err := loadScriptSource(inPath, deps)
			if err != nil {
				return err
			}

			knownMapping, err := loadReadOnlyDict(dictPath)
			if err != nil {
				return err
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

			params := cfg.EntityRecognition
			params.Dictionary = knownMapping.Entities

			transcript := model.TranslateRequest{
				Entity:   knownMapping.Entities,
				Dialogue: extracted.Dialogue,
			}.Transcript()

			obs.Logger.InfoContext(cmd.Context(), "starting entity recognition",
				slog.String("input", inPath),
				slog.String("hashsum", extracted.Hashsum),
				slog.Int("dialogue_units", len(extracted.Dialogue)),
				slog.Int("dictionary_entities", len(knownMapping.Entities)),
			)

			recognized, err := provider.RecognizeEntities(cmd.Context(), transcript, params)
			if err != nil {
				return err
			}
			if recognized == nil {
				recognized = []model.Entity{}
			}

			inputLabel := extracted.Input
			if inputLabel == "" && inPath != "-" {
				inputLabel = inPath
			}
			outBytes, err := marshalIndentedLine(recognizePayload{
				Input:    inputLabel,
				Hashsum:  extracted.Hashsum,
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
	flags.StringVarP(&configPath, "config", "c", "", configFlagUsage)
	flags.StringVarP(&inPath, "in", "i", defaultInputPath, "Path to source script or extracted JSON file ('-' reads from stdin)")
	flags.StringVarP(&dictPath, "dict", "d", "", "Optional read-only path to entity_dict JSON file")
	flags.StringVarP(&outPath, "out", "o", "-", "Output JSON path ('-' writes to stdout)")
	flags.StringVar(&saveRawDir, "save-raw", "", "Optional directory to save raw Responses API JSON bodies as fixtures")
	return cmd
}

func newScriptTranslateCmd(deps cliDeps) *cobra.Command {
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
		Short: "Translate extracted script dialogue to an intermediate translated JSON artifact",
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			inPath = strings.TrimSpace(inPath)
			dictPath = strings.TrimSpace(dictPath)
			entitiesPath = strings.TrimSpace(entitiesPath)

			inExplicit := cmd.Flags().Changed("in")

			var dictMapping model.EntityMapping
			var err error
			if dictPath != "" {
				dictMapping, err = loadReadOnlyDict(dictPath)
				if err != nil {
					return err
				}
			}

			var recEntities []model.Entity
			switch {
			case cmd.Flags().Changed("entities"):
				recEntities, err = loadRecognizeEntities(entitiesPath, deps.stdin, &inPath, inExplicit)
				if err != nil {
					return err
				}
			case dictPath == "" && deps.stdinIsTTY != nil && !deps.stdinIsTTY():
				recEntities, err = loadRecognizeEntities("-", deps.stdin, &inPath, inExplicit)
				if err != nil {
					return err
				}
			}
			entities := mergeEntitiesReadOnly(dictMapping, recEntities)

			if inPath == "" {
				return errors.New("missing input script path: specify --in or pipe output from script recognize")
			}

			extracted, err := loadScriptSource(inPath, deps)
			if err != nil {
				return err
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

			obs.Logger.InfoContext(cmd.Context(), "starting translation",
				slog.String("input", inPath),
				slog.String("hashsum", extracted.Hashsum),
				slog.Int("dialogue_units", len(extracted.Dialogue)),
				slog.Int("entities", len(entities)),
			)

			resp, err := provider.Translate(cmd.Context(), model.TranslateRequest{
				Entity:   entities,
				Dialogue: extracted.Dialogue,
			}, cfg.Translation)
			if err != nil {
				return err
			}

			outBytes, err := marshalIndentedLine(model.TranslatedScript{
				Hashsum:  extracted.Hashsum,
				Dialogue: resp,
			})
			if err != nil {
				return fmt.Errorf("encode translate output: %w", err)
			}

			obs.Logger.InfoContext(cmd.Context(), "translation completed",
				slog.String("input", inPath),
				slog.Int("dialogue_units", len(resp)),
			)
			return writeOutputBytes(outPath, outBytes, deps.stdout)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&configPath, "config", "c", "", configFlagUsage)
	flags.StringVarP(&inPath, "in", "i", defaultInputPath, "Path to source script or extracted JSON file (uses recognize JSON \"input\" when piped and -i is omitted)")
	flags.StringVarP(&dictPath, "dict", "d", "", "Optional read-only path to entity_dict JSON file when not reading recognize output")
	flags.StringVarP(&entitiesPath, "entities", "e", "-", "Path to recognize output JSON ('-' reads stdin when piped)")
	flags.StringVarP(&outPath, "out", "o", "-", "Output translated JSON path ('-' writes to stdout)")
	flags.StringVar(&saveRawDir, "save-raw", "", "Optional directory to save raw Responses API JSON bodies as fixtures")
	return cmd
}

func newScriptAssembleCmd(deps cliDeps) *cobra.Command {
	var (
		inPath         string
		translatedPath string
		dictPath       string
		entitiesPath   string
		outPath        string
		force          bool
	)

	cmd := &cobra.Command{
		Use:   "assemble",
		Short: "Assemble translated intermediate JSON back onto a target script or extracted skeleton",
		RunE: func(cmd *cobra.Command, _ []string) error {
			inPath = strings.TrimSpace(inPath)
			translatedPath = strings.TrimSpace(translatedPath)
			dictPath = strings.TrimSpace(dictPath)
			entitiesPath = strings.TrimSpace(entitiesPath)

			if inPath == "" {
				return errors.New("missing target script path (--in)")
			}

			target, err := loadScriptSource(inPath, deps)
			if err != nil {
				return err
			}

			if !cmd.Flags().Changed("translated") && deps.stdinIsTTY != nil && deps.stdinIsTTY() {
				return errors.New("missing translated JSON input: pipe to stdin or pass --translated")
			}
			if (translatedPath == "" || translatedPath == "-") && entitiesPath == "-" {
				return errors.New("cannot read both --translated and --entities from stdin")
			}

			trBytes, err := readInputBytes(translatedPath, deps.stdin)
			if err != nil {
				return err
			}
			translated, err := decodeTranslatedScript(trBytes)
			if err != nil {
				return err
			}

			if !force && translated.Hashsum != target.Hashsum {
				return fmt.Errorf("transcript hashsum mismatch (target %s, translated %s); pass --force to override",
					target.Hashsum, translated.Hashsum)
			}

			dict, err := loadReadOnlyDict(dictPath)
			if err != nil {
				return err
			}
			var recEntities []model.Entity
			if entitiesPath != "" {
				dummyIn := inPath
				recEntities, err = loadRecognizeEntities(entitiesPath, deps.stdin, &dummyIn, true)
				if err != nil {
					return err
				}
			}
			entities := mergeEntitiesReadOnly(dict, recEntities)

			assembled, err := translate.Assemble(
				target.Dialogue,
				translated.Dialogue,
				translate.RetainedToUnits(target.Retained),
				entities,
			)
			if err != nil {
				return err
			}
			return writeOutputBytes(outPath, []byte(assembled), deps.stdout)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&inPath, "in", "i", defaultInputPath, "Path to target script or extracted JSON skeleton")
	flags.StringVarP(&translatedPath, "translated", "t", "-", "Path to translated JSON artifact ('-' reads from stdin)")
	flags.StringVarP(&dictPath, "dict", "d", "", "Optional read-only path to entity_dict JSON file for speaker mapping")
	flags.StringVarP(&entitiesPath, "entities", "e", "", "Optional recognize JSON file for additional speaker mappings")
	flags.StringVarP(&outPath, "out", "o", defaultOutputPath, "Output translated script path ('-' writes to stdout)")
	flags.BoolVar(&force, "force", false, "Allow assembling even if transcript hashsum differs")
	return cmd
}

func newScriptRunCmd(deps cliDeps) *cobra.Command {
	var (
		configPath string
		inPath     string
		dictPath   string
		outPath    string
		cacheDir   string
		noCache    bool
		saveRawDir string
	)

	cmd := &cobra.Command{
		Use:   "run [script]",
		Short: "Run recognize, translate, and assemble with content-addressed hashsum cache",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			resolvedIn, err := resolveScriptInputArg(cmd, args, inPath)
			if err != nil {
				return err
			}

			extracted, err := loadScriptSource(resolvedIn, deps)
			if err != nil {
				return err
			}

			dictMapping, err := loadReadOnlyDict(dictPath)
			if err != nil {
				return err
			}

			cfg, obs, err := initConfigAndObserver(configPath, deps)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := obs.Close(); cerr != nil {
					runErr = errors.Join(runErr, cerr)
				}
			}()

			effectiveCacheRoot := cfg.CacheDir
			if override := strings.TrimSpace(cacheDir); override != "" {
				effectiveCacheRoot = override
			}
			hashCacheDir := filepath.Join(effectiveCacheRoot, extracted.Hashsum)
			extractedCachePath := filepath.Join(hashCacheDir, "extracted.json")
			recognizedCachePath := filepath.Join(hashCacheDir, "recognized.json")
			translatedCachePath := filepath.Join(hashCacheDir, "translated.json")

			if noCache || !fileExists(extractedCachePath) {
				if err := writeJSONAtomic(extractedCachePath, extracted); err != nil {
					return fmt.Errorf("write extracted cache: %w", err)
				}
			}

			var (
				translated        model.TranslatedScript
				translatedCached  bool
				recognizedPayload recognizePayload
				recognizedCached  bool
			)
			if !noCache {
				translated, translatedCached = tryLoadCachedTranslated(translatedCachePath, extracted.Hashsum)
				recognizedPayload, recognizedCached = tryLoadCachedRecognized(recognizedCachePath, extracted.Hashsum)
			}

			effectiveEntities := mergeEntitiesReadOnly(dictMapping, recognizedPayload.Entities)

			if translatedCached {
				obs.Logger.InfoContext(cmd.Context(), "translation cache hit; skipping LLM calls",
					slog.String("input", resolvedIn),
					slog.String("hashsum", extracted.Hashsum),
					slog.String("cache_dir", hashCacheDir),
				)
			} else {
				provider, err := initProvider(cfg, saveRawDir, "run", obs)
				if err != nil {
					return err
				}

				if recognizedCached {
					obs.Logger.InfoContext(cmd.Context(), "entity recognition cache hit",
						slog.String("hashsum", extracted.Hashsum),
						slog.Int("entities", len(recognizedPayload.Entities)),
					)
				} else {
					params := cfg.EntityRecognition
					params.Dictionary = dictMapping.Entities
					transcript := model.TranslateRequest{
						Entity:   dictMapping.Entities,
						Dialogue: extracted.Dialogue,
					}.Transcript()

					obs.Logger.InfoContext(cmd.Context(), "starting entity recognition",
						slog.String("input", resolvedIn),
						slog.String("hashsum", extracted.Hashsum),
						slog.Int("dialogue_units", len(extracted.Dialogue)),
						slog.Int("dictionary_entities", len(dictMapping.Entities)),
					)

					recognized, err := provider.RecognizeEntities(cmd.Context(), transcript, params)
					if err != nil {
						return err
					}
					if recognized == nil {
						recognized = []model.Entity{}
					}
					recognizedPayload = recognizePayload{
						Input:    extracted.Input,
						Hashsum:  extracted.Hashsum,
						Entities: recognized,
					}
					if err := writeJSONAtomic(recognizedCachePath, recognizedPayload); err != nil {
						return fmt.Errorf("write recognized cache: %w", err)
					}
					obs.Logger.InfoContext(cmd.Context(), "entity recognition completed",
						slog.Int("entities", len(recognized)),
					)
				}

				effectiveEntities = mergeEntitiesReadOnly(dictMapping, recognizedPayload.Entities)

				obs.Logger.InfoContext(cmd.Context(), "starting translation",
					slog.String("input", resolvedIn),
					slog.String("hashsum", extracted.Hashsum),
					slog.Int("dialogue_units", len(extracted.Dialogue)),
					slog.Int("entities", len(effectiveEntities)),
				)

				resp, err := provider.Translate(cmd.Context(), model.TranslateRequest{
					Entity:   effectiveEntities,
					Dialogue: extracted.Dialogue,
				}, cfg.Translation)
				if err != nil {
					return err
				}

				translated = model.TranslatedScript{
					Hashsum:  extracted.Hashsum,
					Dialogue: resp,
				}
				if err := writeJSONAtomic(translatedCachePath, translated); err != nil {
					return fmt.Errorf("write translated cache: %w", err)
				}
				obs.Logger.InfoContext(cmd.Context(), "translation completed",
					slog.String("input", resolvedIn),
					slog.String("hashsum", extracted.Hashsum),
					slog.Int("dialogue_units", len(resp)),
				)
			}

			assembled, err := translate.Assemble(
				extracted.Dialogue,
				translated.Dialogue,
				translate.RetainedToUnits(extracted.Retained),
				effectiveEntities,
			)
			if err != nil {
				return err
			}
			return writeOutputBytes(outPath, []byte(assembled), deps.stdout)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&configPath, "config", "c", "", configFlagUsage)
	flags.StringVarP(&inPath, "in", "i", defaultInputPath, "Path to source script or extracted JSON file ('-' reads from stdin)")
	flags.StringVarP(&dictPath, "dict", "d", "", "Optional read-only path to entity_dict JSON file (never modified)")
	flags.StringVarP(&outPath, "out", "o", defaultOutputPath, "Output translated script path ('-' writes to stdout)")
	flags.StringVar(&cacheDir, "cache-dir", "", "Override config cache_dir for hashsum intermediate cache")
	flags.BoolVar(&noCache, "no-cache", false, "Ignore existing cached recognition/translation results and refresh cache")
	flags.StringVar(&saveRawDir, "save-raw", "", "Optional directory to save raw Responses API JSON bodies as fixtures")
	return cmd
}

func resolveScriptInputArg(cmd *cobra.Command, args []string, inPath string) (string, error) {
	if len(args) == 1 {
		if cmd.Flags().Changed("in") {
			return "", errors.New("cannot combine positional input path with --in")
		}
		inPath = args[0]
	}
	inPath = strings.TrimSpace(inPath)
	if inPath == "" {
		return "", errors.New("missing input script path (--in)")
	}
	return inPath, nil
}

func loadScriptSource(inPath string, deps cliDeps) (model.ExtractedScript, error) {
	raw, err := readInputBytes(inPath, deps.stdin)
	if err != nil {
		return model.ExtractedScript{}, err
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.HasPrefix(trimmed, []byte("{")) {
		var extracted model.ExtractedScript
		if err := json.Unmarshal(trimmed, &extracted); err != nil {
			return model.ExtractedScript{}, fmt.Errorf("parse extracted JSON %q: %w", inPath, err)
		}
		if extracted.Hashsum == "" || extracted.Dialogue == nil {
			return model.ExtractedScript{}, fmt.Errorf("extracted JSON %q missing required fields (hashsum/dialogue)", inPath)
		}
		return extracted, nil
	}

	parsed, err := translate.ParseBytes(raw)
	if err != nil {
		return model.ExtractedScript{}, fmt.Errorf("read script %q: %w", inPath, err)
	}
	inputLabel := inPath
	if inputLabel == "-" {
		inputLabel = ""
	}
	extracted, warnings, err := translate.ExtractScriptWithWarnings(inputLabel, parsed)
	if err != nil {
		return model.ExtractedScript{}, err
	}
	for _, w := range warnings {
		if _, werr := fmt.Fprintf(deps.stderr, "warning: %s\n", w); werr != nil {
			return model.ExtractedScript{}, werr
		}
	}
	return extracted, nil
}

func loadReadOnlyDict(dictPath string) (model.EntityMapping, error) {
	d := strings.TrimSpace(dictPath)
	if d == "" {
		return model.EntityMapping{Entities: []model.Entity{}}, nil
	}
	return entitystore.Load(d)
}

// mergeEntitiesReadOnly combines authoritative dict entries with recognized entities
// strictly in memory. Existing dictionary entries keep their canonical/alias translations;
// newly discovered canonicals or aliases from recognized are appended without modifying dict.
func mergeEntitiesReadOnly(dict model.EntityMapping, recognized []model.Entity) []model.Entity {
	out := make([]model.Entity, 0, len(dict.Entities)+len(recognized))
	byCanonical := make(map[string]int, len(dict.Entities))
	claimedTerms := make(map[string]struct{}, len(dict.Entities)*2)

	for _, e := range dict.Entities {
		copied := model.Entity{
			ID:        e.ID,
			Canonical: e.Canonical,
			Aliases:   append([]model.NamePair(nil), e.Aliases...),
		}
		byCanonical[copied.Canonical.From] = len(out)
		claimedTerms[copied.Canonical.From] = struct{}{}
		for _, a := range copied.Aliases {
			claimedTerms[a.From] = struct{}{}
		}
		out = append(out, copied)
	}

	for _, rec := range recognized {
		from := strings.TrimSpace(rec.Canonical.From)
		to := strings.TrimSpace(rec.Canonical.To)
		if from == "" || to == "" {
			continue
		}
		if idx, exists := byCanonical[from]; exists {
			for _, a := range rec.Aliases {
				af := strings.TrimSpace(a.From)
				at := strings.TrimSpace(a.To)
				if af == "" || at == "" {
					continue
				}
				if _, used := claimedTerms[af]; used {
					continue
				}
				out[idx].Aliases = append(out[idx].Aliases, model.NamePair{From: af, To: at})
				claimedTerms[af] = struct{}{}
			}
			continue
		}
		if _, used := claimedTerms[from]; used {
			continue
		}
		created := model.Entity{
			Canonical: model.NamePair{From: from, To: to},
			Aliases:   []model.NamePair{},
		}
		claimedTerms[from] = struct{}{}
		for _, a := range rec.Aliases {
			af := strings.TrimSpace(a.From)
			at := strings.TrimSpace(a.To)
			if af == "" || at == "" {
				continue
			}
			if _, used := claimedTerms[af]; used {
				continue
			}
			created.Aliases = append(created.Aliases, model.NamePair{From: af, To: at})
			claimedTerms[af] = struct{}{}
		}
		byCanonical[from] = len(out)
		out = append(out, created)
	}
	return out
}

func loadRecognizeEntities(path string, stdin io.Reader, inPath *string, inExplicit bool) ([]model.Entity, error) {
	raw, err := readInputBytes(path, stdin)
	if err != nil {
		return nil, err
	}
	payload, err := decodeRecognizePayload(raw)
	if err != nil {
		return nil, err
	}
	if !inExplicit && strings.TrimSpace(payload.Input) != "" {
		*inPath = strings.TrimSpace(payload.Input)
	}
	return payload.Entities, nil
}

func decodeTranslatedScript(data []byte) (model.TranslatedScript, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return model.TranslatedScript{}, errors.New("empty translated JSON input")
	}
	var payload model.TranslatedScript
	if err := json.Unmarshal(data, &payload); err != nil {
		return model.TranslatedScript{}, fmt.Errorf("parse translated JSON: %w", err)
	}
	if payload.Hashsum == "" || payload.Dialogue == nil {
		return model.TranslatedScript{}, errors.New("translated JSON missing required fields (hashsum/dialogue)")
	}
	return payload, nil
}

func tryLoadCachedTranslated(path, wantHash string) (model.TranslatedScript, bool) {
	raw, err := os.ReadFile(path) // #nosec G304 -- cache file path is constructed from validated hashsum
	if err != nil {
		return model.TranslatedScript{}, false
	}
	payload, err := decodeTranslatedScript(raw)
	if err != nil || payload.Hashsum != wantHash {
		return model.TranslatedScript{}, false
	}
	return payload, true
}

func tryLoadCachedRecognized(path, wantHash string) (recognizePayload, bool) {
	raw, err := os.ReadFile(path) // #nosec G304 -- cache file path is constructed from validated hashsum
	if err != nil {
		return recognizePayload{}, false
	}
	payload, err := decodeRecognizePayload(raw)
	if err != nil || (payload.Hashsum != "" && payload.Hashsum != wantHash) {
		return recognizePayload{}, false
	}
	return payload, true
}

func writeJSONAtomic(path string, v any) error {
	b, err := marshalIndentedLine(v)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
