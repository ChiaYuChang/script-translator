package translate

import "errors"

var (
	ErrEmptyScript           = errors.New("empty script")
	ErrInvalidDialogueFormat = errors.New("invalid dialogue format")
	ErrNoTranslationUnit     = errors.New("no translation unit")
)
