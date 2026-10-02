package notify

import (
	"context"
	"errors"
	"net"
	"strings"
)

// FormatError renders err for a notification body: the raw error followed
// by a "Reason:" line explaining the likely cause and what to check.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	return "Error: " + err.Error() + "\n\nReason: " + Explain(err)
}

const (
	reasonTimeout = "The request took too long: the LLM server did not respond in time. It may be overloaded, still loading the model, or the prompt is too large."
	reasonNetwork = "A network error occurred while talking to the LLM server. Check that it is running and reachable."
	reasonUnknown = "Unexpected error with no known cause. See the raw error above and the terminal output / output log for details."
)

// explainRules are checked in order against the lowercased error text; the
// first match wins, so more specific patterns come before general ones.
var explainRules = []struct {
	substrs []string // any match
	reason  string
}{
	{[]string{"requirements.md not found"},
		"There is no requirements.md in the directory jonnyq was started in. Start jonnyq from the project folder or create requirements.md first."},
	{[]string{".progress not found"},
		"There is no task list yet. Run /plan first to generate .progress from requirements.md."},
	{[]string{"model did not create .progress"},
		"The model finished its turn without writing .progress, usually because it answered in text instead of calling write_file, or the tool call arguments were malformed. Try again, switch /toolmode to prompt, or use a more capable model."},
	{[]string{"no model set"},
		"No model is configured. Use /model <name> (or -model) before running prompts."},
	{[]string{"made no progress after"},
		"The model kept retrying the same task but never checked it off in .progress (typically it claims success without editing the file, edit_file did not match, or the build/tests keep failing). Check .progress and requirements.md manually and fix or split the task."},
	{[]string{"has been completed in the last"},
		"Many rounds passed without any task being checked off, so the run was stopped to avoid looping forever. The model may be rewriting .progress without finishing work. Inspect .progress and requirements.md."},
	{[]string{"tool call budget exceeded"},
		"The model made too many tool calls in a single turn, which usually means it is stuck in a loop. Break the task into smaller steps or retry."},
	{[]string{"stream ended without a completion event"},
		"The connection to the LLM server dropped mid-response. The server may have crashed or run out of memory (e.g. context size too large for the model), or a proxy closed the connection. Check the server logs."},
	{[]string{"unexpected status 401", "unexpected status 403"},
		"The provider rejected the credentials. The API key is missing, wrong or expired; set it with /key or -key."},
	{[]string{"unexpected status 404", "model not found", "try pulling it first"},
		"The provider could not find the endpoint or model. Check that the model name is correct and installed (/list_model) and that the provider URL is right."},
	{[]string{"unexpected status 429", "rate limit"},
		"The provider is rate limiting requests or quota is exhausted. Wait and retry, or check your plan limits."},
	{[]string{"unexpected status 400", "context length", "context window", "maximum context"},
		"The provider rejected the request as invalid. Most often the conversation exceeds the model's context size; lower the context setting or start a fresh session."},
	{[]string{"unexpected status 5"},
		"The LLM server returned a server-side error or is overloaded/restarting (for example while loading a model). Check its logs and retry."},
	{[]string{"connection refused"},
		"Nothing is listening at the provider URL. The LLM server (Ollama / llama.cpp / API gateway) is probably not running, or the host/port in /provider is wrong."},
	{[]string{"no such host"},
		"The provider hostname could not be resolved. Check the URL set with /provider and the network/DNS connectivity."},
	{[]string{"connection reset", "broken pipe", "unexpected eof"},
		"The connection to the LLM server was closed unexpectedly, which often means the server crashed or restarted mid-request. Check the server logs."},
	{[]string{"x509", "tls:", "certificate"},
		"A TLS/certificate problem prevented connecting to the provider. Check the URL scheme (http vs https) and certificate validity."},
	{[]string{"deadline exceeded", "timeout", "timed out"}, reasonTimeout},
	{[]string{"permission denied"},
		"The operating system denied access to a file or command. Check file permissions in the working directory."},
	{[]string{"no space left"},
		"The disk is full, so files or logs could not be written."},
}

// Explain returns a short plain-language description of the most likely
// cause of err and what to check, matched heuristically on the error
// text/type. It always returns something useful, falling back to a generic
// hint for unrecognized errors.
func Explain(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return reasonTimeout
	}
	msg := strings.ToLower(err.Error())
	for _, r := range explainRules {
		for _, s := range r.substrs {
			if strings.Contains(msg, s) {
				return r.reason
			}
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return reasonTimeout
		}
		return reasonNetwork
	}
	return reasonUnknown
}
