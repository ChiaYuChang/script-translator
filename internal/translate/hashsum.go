package translate

import (
	"crypto/sha256"
	"encoding/hex"

	"go-script-translate/internal/model"
)

// TranscriptHashsum computes the lowercase hex SHA-256 digest of a script's
// extracted transcript, ignoring metadata such as commands, comments,
// blank lines, and line numbers.
func TranscriptHashsum(script model.Script) (string, error) {
	units, _, err := Extract(script)
	if err != nil {
		return "", err
	}
	return HashsumFromUnits(units), nil
}

// HashsumFromUnits computes the lowercase hex SHA-256 digest from already
// extracted dialogue units.
func HashsumFromUnits(units []model.TranslateUnit) string {
	transcript := model.TranslateRequest{Dialogue: units}.Transcript()
	sum := sha256.Sum256([]byte(transcript))
	return hex.EncodeToString(sum[:])
}
