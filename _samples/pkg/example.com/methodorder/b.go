package methodorder

// B has two methods, declared so that the one referencing A.M comes second.
// Regression: a previous bug caused MethodByName to prefer the package-qualified
// alias (e.g. "example.com/methodorder.A") over the short name "A" when the
// receiver type's Name was empty. Methods are registered under the short name
// (e.g. "*A.M"), so the lookup missed and resolution failed with "undefined: M".

type B struct{ A A }

func (b *B) N() int { return 0 }

func (b *B) Call() int { return b.A.M() }
