package bodyless

type Duration int64

// Sleep is a body-less function declaration (runtime-linked).
func Sleep(d Duration)

// Now has a normal body.
func Now() int { return 0 }
