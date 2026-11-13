package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

type ScriptLineType uint8

const (
	ScriptLineTypeEmpty    ScriptLineType = 0
	ScriptLineTypeComment  ScriptLineType = 1
	ScriptLineTypeTag      ScriptLineType = 2
	ScriptLineTypeDialogue ScriptLineType = 3
)

func (st ScriptLineType) String() string {
	switch st {
	case ScriptLineTypeEmpty:
		return "EMPTY"
	case ScriptLineTypeComment:
		return "COMMENT"
	case ScriptLineTypeTag:
		return "TAG"
	case ScriptLineTypeDialogue:
		return "DIALOGUE"
	}
	return "UNKNONW"
}

var (
	RegexComment  = regexp.MustCompile("^//(.*)")
	RegexCommand  = regexp.MustCompile("^<([A-Z_]+)>(.*)$")
	RegexDialogue = regexp.MustCompile("「(.+)」")
)

type Script struct {
	N    int
	Raw  []string
	Line []ScriptLine
}

type ScriptLine struct {
	Index   int
	Type    ScriptLineType
	Label   string
	Content string
}

func (r ScriptLine) String() string {
	return fmt.Sprintf("%04d %20s %s", r.Index, r.Label, r.Content)
}

type Dialogue struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
}

func (d Dialogue) String() string {
	b, _ := json.Marshal(d)
	return string(b)
}

func ParseScriptFile(f *os.File) ([]ScriptLine, error) {
	buff, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(buff), "\n")
	script := make([]ScriptLine, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		s := ScriptLine{Index: i}
		if len(line) == 0 {
			s.Label = "EMPTY"
			s.Type = ScriptLineTypeEmpty
			script = append(script, s)
		} else {
			if RegexComment.MatchString(line) {
				s.Type = ScriptLineTypeComment
				m := RegexComment.FindStringSubmatch(line)
				s.Label = "COMMENT"
				s.Content = m[1]
			} else if RegexCommand.MatchString(line) {
				s.Type = ScriptLineTypeTag
				m := RegexCommand.FindStringSubmatch(line)
				s.Label = m[1]
				s.Content = m[2]
			} else {
				s.Type = ScriptLineTypeDialogue
				s.Label = "DIALOGUE"
				s.Content = line
			}
		}
		script[i] = s
	}
	return script, nil
}

func ExtractDialogue(script []ScriptLine) ([]Dialogue, error) {
	dialogue := []Dialogue{}
	entity := map[string]string{}
	for i := 0; i < len(script); i++ {
		s := script[i]
		if s.Type == ScriptLineTypeTag && s.Label == "NAME_PLATE" {
			builder := &strings.Builder{}
			if s.Content != "" {
				entity[s.Content] = ""
			}

			var j int
			for j = i + 1; j < len(script); j++ {
				s := script[j]
				if s.Type != ScriptLineTypeDialogue {
					break
				}
				builder.WriteString(s.Content)
			}

			c := builder.String()
			m := RegexDialogue.FindStringSubmatch(c)
			if len(m) > 1 {
				for _, c := range m[1:] {
					d := Dialogue{Index: 0, Name: s.Content}
					d.Content = c
					dialogue = append(dialogue, d)
				}
			} else {
				d := Dialogue{Index: 0, Name: "Off-Screen", Content: c}
				dialogue = append(dialogue, d)
			}
			i = j - 1
		}
	}

	for i, _ := range dialogue {
		dialogue[i].Index = i
	}
	return dialogue, nil
}

func main() {
	var i string
	var o string
	flag.StringVar(&i, "i", "original.txt", "script to translate")
	flag.StringVar(&o, "o", "translated.txt", "translated script")
	flag.Parse()

	f, err := os.Open(i)
	if err != nil {
		slog.Error(fmt.Sprintf("cannot open file %s: %v", i, err))
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn(fmt.Sprintf("error while closing file %s: %v", i, err))
		}
	}()

	script, err := ParseScriptFile(f)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to parse script file: %v", err))
		os.Exit(1)
	}

	dialogue, err := ExtractDialogue(script)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to extract dialogue: %v", err))
		os.Exit(1)
	}

	hasher := sha256.New()
	entity := map[string]string{}
	for _, d := range dialogue {
		hasher.Write([]byte(d.String()))
		entity[d.Name] = ""
		fmt.Println(d.String())
	}

	fmt.Println(entity)
	fmt.Printf("sha256sum: %s\n", base64.RawStdEncoding.EncodeToString(hasher.Sum(nil)))
}
