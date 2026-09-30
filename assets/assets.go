// Package assets embeds the files the executable needs at run time.
package assets

import _ "embed"

// IconFont is Material Icons Round (Apache-2.0), see fonts/README.md.
//
//go:embed fonts/MaterialIconsRound-Regular.otf
var IconFont []byte

// Inter (SIL Open Font License 1.1), the interface typeface, in the four weights the design uses.
var (
	//go:embed fonts/Inter-Regular.ttf
	InterRegular []byte
	//go:embed fonts/Inter-Medium.ttf
	InterMedium []byte
	//go:embed fonts/Inter-SemiBold.ttf
	InterSemiBold []byte
	//go:embed fonts/Inter-Bold.ttf
	InterBold []byte
)
