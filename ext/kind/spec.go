// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package kind

// This file has the optional interfaces a Spec implements to tell the core more than how to
// render it, in the manner of SecretUser: the core asks for each with a type assertion and never
// looks inside a Spec otherwise.

// Referrer is implemented by a Spec that depends on other resources of its repository by code:
// the skills and agents the steps of a workflow use, the resources an agent binds. The core
// refuses a resource that refers to a code its repository does not have, or to one that cannot
// be read, the way it refuses one that names an undefined secret.
type Referrer interface {
	// Refs returns the codes of the resources it depends on, sorted, each once.
	Refs() []string
}

// LocalKeeper is implemented by a Spec with directories whose content, once delivered, belongs
// to the person's computer, such as the memory of an agent. They do not count towards the
// resource's content digest. A Render function that delivers them puts them in
// adapter.SkillDir.Local, so that an update keeps what is in them.
type LocalKeeper interface {
	// Local returns the directories, relative to the resource's root with forward slashes and
	// without a trailing slash, sorted.
	Local() []string
}

// Flow is implemented by a Spec that is a sequence of steps, which the command line hands out one
// at a time: a step is only given to the AI once the checks of the step before it have passed.
// The core keeps the steps on the person's computer when it syncs the resource; the artifact
// the kind renders only tells the AI how to ask for the next step.
type Flow interface {
	// Steps returns the steps in order. There is at least one.
	Steps() []Step
}

// Step is one step of a Flow.
type Step struct {
	// Use is the code of the skill or agent that does the step.
	Use string
	// With are the codes of further resources the step uses, such as a knowledge base.
	With []string
	// Checks are what has to hold before the next step is handed out.
	Checks []Check
	// Forbid lists what must not be done in this step, as text for the AI.
	Forbid []string
	// Then is ThenContinue or ThenConfirm.
	Then string
}

// The values of Step.Then.
const (
	// ThenContinue: once the checks have passed, the next step is handed out.
	ThenContinue = "continue"
	// ThenConfirm: once the checks have passed, the next step waits for the person to say so.
	ThenConfirm = "confirm"
)

// Check is one thing that has to hold at the end of a step.
type Check struct {
	// Text says what has to hold, for the AI and the person.
	Text string
	// By is who decides: CheckByAI, CheckByCommand or CheckByHuman.
	By string
	// Run is the command of a CheckByCommand check. It is run by the system's shell in the
	// working directory of the person's project; it passes when it exits with 0.
	Run string
}

// The values of Check.By.
const (
	// CheckByAI: the AI checks it and states its evidence.
	CheckByAI = "ai"
	// CheckByCommand: the command line runs Check.Run.
	CheckByCommand = "command"
	// CheckByHuman: the person confirms it.
	CheckByHuman = "human"
)

// Pulled is implemented by a Spec whose content is not in the repository: the repository only
// points at it, and the command line fetches it onto the person's computer with the person's own
// access, such as a knowledge base kept in a directory of another Git repository. Its content
// never passes through the server.
type Pulled interface {
	// Pull returns where the content is.
	Pull() GitSource
}

// GitSource is a directory of a Git repository.
type GitSource struct {
	// URL is the repository's address: https or ssh.
	URL string
	// Ref is a branch, a tag or a commit; empty means the repository's default branch.
	Ref string
	// Dir is the directory inside the repository, with forward slashes; empty means all of it.
	Dir string
}
