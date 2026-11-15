package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"go-script-translate/internal/entitystore"
	"go-script-translate/internal/model"
	"go-script-translate/internal/pathutil"
)

func newEntityCmd(deps cliDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "entity",
		Short: "Manage persistent entity_dict mappings",
	}
	cmd.AddCommand(newEntityInsertCmd(deps))
	cmd.AddCommand(newEntityListCmd(deps))
	return cmd
}

func newEntityInsertCmd(deps cliDeps) *cobra.Command {
	var (
		dictPath string
		inPath   string
		fromName string
		toName   string
	)

	cmd := &cobra.Command{
		Use:     "insert",
		Aliases: []string{"import"},
		Short:   "Insert or merge entities into entity_dict from recognize JSON or --from/--to",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			dictPath, err = pathutil.ExpandHome(dictPath)
			if err != nil {
				return fmt.Errorf("resolve dictionary path: %w", err)
			}
			if dictPath == "" {
				return errors.New("missing dictionary path (--dict)")
			}

			hasInline := cmd.Flags().Changed("from") || cmd.Flags().Changed("to")
			if hasInline && cmd.Flags().Changed("in") {
				return errors.New("cannot combine --from/--to with --in")
			}

			var reviewed model.EntityMapping
			if hasInline {
				from := strings.TrimSpace(fromName)
				to := strings.TrimSpace(toName)
				if from == "" || to == "" {
					return errors.New("both --from and --to must be non-empty")
				}
				reviewed = model.EntityMapping{
					Entities: []model.Entity{{
						Canonical: model.NamePair{From: from, To: to},
						Aliases:   []model.NamePair{},
					}},
				}
			} else {
				if !cmd.Flags().Changed("in") && deps.stdinIsTTY != nil && deps.stdinIsTTY() {
					return errors.New("missing input: pipe recognize JSON to stdin or pass --in / --from --to")
				}
				raw, err := readInputBytes(inPath, deps.stdin)
				if err != nil {
					return err
				}
				payload, err := decodeRecognizePayload(raw)
				if err != nil {
					return err
				}
				reviewed = model.EntityMapping{Entities: payload.Entities}
			}

			current, err := entitystore.Load(dictPath)
			if err != nil {
				return err
			}

			merged, err := entitystore.ImportReviewed(current, reviewed)
			if err != nil {
				return err
			}
			if err := entitystore.Save(dictPath, merged); err != nil {
				return err
			}

			_, err = fmt.Fprintf(deps.stdout, "saved %d entities to %s (processed %d input entities)\n",
				len(merged.Entities), dictPath, len(reviewed.Entities))
			return err
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&dictPath, "dict", "d", "", "Path to entity_dict JSON file")
	flags.StringVarP(&inPath, "in", "i", "-", "Path to recognize/entity JSON file ('-' reads from stdin)")
	flags.StringVar(&fromName, "from", "", "Insert a single Japanese canonical name (requires --to)")
	flags.StringVar(&toName, "to", "", "Traditional Chinese translation for --from")
	_ = cmd.MarkFlagRequired("dict")
	return cmd
}

func newEntityListCmd(deps cliDeps) *cobra.Command {
	var (
		dictPath string
		asJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List entities stored in entity_dict",
		RunE: func(_ *cobra.Command, _ []string) error {
			var err error
			dictPath, err = pathutil.ExpandHome(dictPath)
			if err != nil {
				return fmt.Errorf("resolve dictionary path: %w", err)
			}
			if dictPath == "" {
				return errors.New("missing dictionary path (--dict)")
			}

			mapping, err := entitystore.Load(dictPath)
			if err != nil {
				return err
			}

			if asJSON {
				data, err := marshalIndentedLine(mapping)
				if err != nil {
					return fmt.Errorf("encode entity list JSON: %w", err)
				}
				_, err = deps.stdout.Write(data)
				return err
			}

			for _, e := range mapping.Entities {
				if _, err := fmt.Fprintf(deps.stdout, "#%d\n%s\n", e.ID, e.String()); err != nil {
					return err
				}
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&dictPath, "dict", "d", "", "Path to entity_dict JSON file")
	flags.BoolVar(&asJSON, "json", false, "Output entities as JSON")
	_ = cmd.MarkFlagRequired("dict")
	return cmd
}
