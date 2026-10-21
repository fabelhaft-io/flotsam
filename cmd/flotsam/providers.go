package main

// Every built-in provider is enabled by a blank import below. Importing a
// provider package runs its init function, which registers it. This file is
// the only place in the core that names providers, so adding one never means
// editing core code, and a custom build can include exactly the providers it
// needs by changing this list.
import (
	_ "github.com/fabelhaft-io/flotsam/providers/github"
)
