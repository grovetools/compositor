package ghostty

import "github.com/grovetools/tuimux"

// KittyKeyToGhostty maps a KittyKeyMsg to the ghostty key encoder's
// key constant, modifier bitmask, and UTF-8 text.
func KittyKeyToGhostty(msg tuimux.KittyKeyMsg) (key int, mods int, utf8Text string) {
	mods = KittyModsToGhostty(msg.Mods)

	switch {
	case msg.Keycode >= 'a' && msg.Keycode <= 'z':
		key = KeyA + (msg.Keycode - 'a')
		utf8Text = string(rune(msg.Keycode))
	case msg.Keycode >= 'A' && msg.Keycode <= 'Z':
		key = KeyA + (msg.Keycode - 'A')
		utf8Text = string(rune(msg.Keycode))
	case msg.Keycode >= '0' && msg.Keycode <= '9':
		key = KeyDigit0 + (msg.Keycode - '0')
		utf8Text = string(rune(msg.Keycode))
	case msg.Keycode == 9:
		key = KeyTab
	case msg.Keycode == 13:
		key = KeyEnter
	case msg.Keycode == 27:
		key = KeyEscape
	case msg.Keycode == 127:
		key = KeyBackspace
	case msg.Keycode == 32:
		key = KeySpace
		utf8Text = " "
	case msg.Keycode == '.':
		key = KeyPeriod
		utf8Text = "."
	case msg.Keycode == ',':
		key = KeyComma
		utf8Text = ","
	case msg.Keycode == '/':
		key = KeySlash
		utf8Text = "/"
	case msg.Keycode == ';':
		key = KeySemicolon
		utf8Text = ";"
	case msg.Keycode == '\'':
		key = KeyQuote
		utf8Text = "'"
	case msg.Keycode == '[':
		key = KeyBracketLeft
		utf8Text = "["
	case msg.Keycode == ']':
		key = KeyBracketRight
		utf8Text = "]"
	case msg.Keycode == '\\':
		key = KeyBackslash
		utf8Text = "\\"
	case msg.Keycode == '`':
		key = KeyBackquote
		utf8Text = "`"
	case msg.Keycode == '-':
		key = KeyMinus
		utf8Text = "-"
	case msg.Keycode == '=':
		key = KeyEqual
		utf8Text = "="
	default:
		if msg.Keycode >= 32 && msg.Keycode < 127 {
			key = KeyUnidentified
			utf8Text = string(rune(msg.Keycode))
		} else {
			key = KeyUnidentified
		}
	}
	return
}

// KittyModsToGhostty converts kitty keyboard protocol modifier bits to
// the ghostty modifier bitmask.
func KittyModsToGhostty(kittyMods int) int {
	var gm int
	if kittyMods&1 != 0 {
		gm |= ModShift
	}
	if kittyMods&2 != 0 {
		gm |= ModAlt
	}
	if kittyMods&4 != 0 {
		gm |= ModCtrl
	}
	if kittyMods&8 != 0 {
		gm |= ModSuper
	}
	return gm
}
