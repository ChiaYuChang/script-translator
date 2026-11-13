package translate

import "regexp"

var (
	RegexNote = regexp.MustCompile("^//(.*)")
	RegexCmd  = regexp.MustCompile("^<(/?[A-Z_]+)>(.*)$")
	RegexText = regexp.MustCompile(`「([^「」]+)」`)
)
