package pkg2

import "example.com/pkg1"

var W = pkg1.V + " world"

func G() int { return pkg1.F() + 1 }
