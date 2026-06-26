/*
Package selfimportpkg has a doc comment whose example self-imports the package:

	import "selfimportpkg"
	v := selfimportpkg.Answer

The naive import scanner once harvested that quoted path (it does not strip
block comments), registering the package as its own Bin bridge and blanking the
extracted bindings. See extract's self-import guard.
*/
package selfimportpkg

type Thing int

func Answer() int { return 42 }
