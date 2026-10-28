package tools

import internaltools "github.com/makkalot/caretz/internal/tools"

const MaxToolOutputBytes = internaltools.MaxToolOutputBytes

type Tool = internaltools.Tool
type ToolHandler = internaltools.ToolHandler
type Config = internaltools.Config
type ToolConfigValidator = internaltools.ToolConfigValidator
type FileSender = internaltools.FileSender
type VerificationResult = internaltools.VerificationResult

var WithSendFileMessageID = internaltools.WithSendFileMessageID

// IsPathSafe reports whether filePath stays inside cwd, and why not when it
// does not.
var IsPathSafe = internaltools.IsPathSafe

// IsBinaryContent reports whether a file's content is binary, as the Read
// tool decides it.
var IsBinaryContent = internaltools.IsBinaryContent
