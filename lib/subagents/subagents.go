package subagents

import internalsubagents "github.com/makkalot/caretz/internal/subagents"

type Frontmatter = internalsubagents.Frontmatter
type Definition = internalsubagents.Definition

// Agent modes a definition's frontmatter may declare.
const (
	ModeSubagent = internalsubagents.ModeSubagent
	ModePeer     = internalsubagents.ModePeer
)

var DiscoverFromDirs = internalsubagents.DiscoverFromDirs

// LoadFromDirs loads the definition called name, with later directories
// overriding earlier ones.
var LoadFromDirs = internalsubagents.LoadFromDirs

// Parse checks a definition file's content the way discovery does and
// returns its frontmatter and prompt body.
var Parse = internalsubagents.Parse
