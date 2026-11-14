package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestTranscriptHashsumIgnoresMetadata(t *testing.T) {
	base := strings.Join([]string{
		"// 作成者：DEMO_AUTHOR",
		"<BG>nc_cafe_morning,IN,1000",
		"<NAME_PLATE>",
		"昼休みの少し前。",
		"<PAUSE>",
		"<NAME_PLATE>新堂ミナト",
		"「それは言わないで。",
		"　時計を見るたびに」",
		"「時間が止まってる気がするんだ」",
		"<PAUSE>",
	}, "\n") + "\n"

	variant := strings.Join([]string{
		"\ufeff// 作成者：ANOTHER_AUTHOR\r",
		"// extra comment\r",
		"<BGM_PLAY>bgm_01,500\r",
		"\r",
		"  <NAME_PLATE>  \r",
		"  昼休みの少し前。  \r",
		"\r",
		"<ACTOR>0,chr_minato_a,LEFT,IN,1000\r",
		"<NAME_PLATE>EMPTY_IGNORED\r",
		"<PAUSE>\r",
		"<NAME_PLATE>新堂ミナト\r",
		"「それは言わないで。\r",
		"　時計を見るたびに」\r",
		"<VOICE>minato_001\r",
		"<NAME_PLATE>新堂ミナト\r",
		"「時間が止まってる気がするんだ」\r",
		"// trailing note\r",
	}, "\n")

	baseScript, err := ParseBytes([]byte(base))
	if err != nil {
		t.Fatalf("ParseBytes(base) failed: %v", err)
	}
	variantScript, err := ParseBytes([]byte(variant))
	if err != nil {
		t.Fatalf("ParseBytes(variant) failed: %v", err)
	}

	baseSum, err := TranscriptHashsum(baseScript)
	if err != nil {
		t.Fatalf("TranscriptHashsum(base) failed: %v", err)
	}
	variantSum, err := TranscriptHashsum(variantScript)
	if err != nil {
		t.Fatalf("TranscriptHashsum(variant) failed: %v", err)
	}

	if baseSum != variantSum {
		t.Fatalf("expected matching hashsums when only metadata differs:\nbase:    %s\nvariant: %s", baseSum, variantSum)
	}

	wantTranscript := "OS:昼休みの少し前。\n新堂ミナト:それは言わないで。時計を見るたびに\n新堂ミナト:時間が止まってる気がするんだ"
	rawSum := sha256.Sum256([]byte(wantTranscript))
	wantHex := hex.EncodeToString(rawSum[:])
	if baseSum != wantHex {
		t.Fatalf("hashsum mismatch: got %s, want %s", baseSum, wantHex)
	}
}

func TestTranscriptHashsumDetectsContentChanges(t *testing.T) {
	a := "<NAME_PLATE>新堂ミナト\n「おはよう」\n"
	b := "<NAME_PLATE>新堂ミナト\n「こんにちは」\n"
	c := "<NAME_PLATE>朝倉ユイ\n「おはよう」\n"

	sumA := mustHashsum(t, a)
	sumB := mustHashsum(t, b)
	sumC := mustHashsum(t, c)

	if sumA == sumB {
		t.Fatalf("expected different hashsum when dialogue text changes: %s", sumA)
	}
	if sumA == sumC {
		t.Fatalf("expected different hashsum when speaker changes: %s", sumA)
	}
}

func TestTranscriptHashsumErrors(t *testing.T) {
	script, err := ParseBytes([]byte("<BG>morning\n// comment only\n"))
	if err != nil {
		t.Fatalf("ParseBytes failed: %v", err)
	}
	if _, err := TranscriptHashsum(script); !errors.Is(err, ErrNoTranslationUnit) {
		t.Fatalf("expected ErrNoTranslationUnit, got %v", err)
	}

	if _, err := ParseBytes([]byte("  \n\r\n")); !errors.Is(err, ErrEmptyScript) {
		t.Fatalf("expected ErrEmptyScript, got %v", err)
	}
}

func mustHashsum(t *testing.T, content string) string {
	t.Helper()
	script, err := ParseBytes([]byte(content))
	if err != nil {
		t.Fatalf("ParseBytes failed: %v", err)
	}
	sum, err := TranscriptHashsum(script)
	if err != nil {
		t.Fatalf("TranscriptHashsum failed: %v", err)
	}
	return sum
}
