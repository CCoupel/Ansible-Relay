#!/usr/bin/env python3
"""
convert_python_to_go.py — Automated Python → GO code conversion via Claude API

Usage:
  python3 convert_python_to_go.py \
    --input server/api/routes_register.py \
    --output server/pkg/handlers/register.go

Dependencies:
  pip install anthropic
"""

import argparse
import sys
import os
from pathlib import Path

try:
    import anthropic
except ImportError:
    print("ERROR: anthropic not installed. Run: pip install anthropic", file=sys.stderr)
    sys.exit(1)


SYSTEM_PROMPT = """You are an expert GO programmer converting Python code to idiomatic GO.

## Conversion Rules

### Error Handling
- Python exceptions → GO error returns
- Use `if err != nil` pattern consistently
- Custom error types for domain errors
- Wrap errors with context: `fmt.Errorf("operation failed: %w", err)`

### Concurrency
- Python `async/await` → GO goroutines + channels
- Python threads → GO goroutines (lighter weight)
- Use `context.Context` for cancellation + timeouts
- Prefer channels over mutexes for simple synchronization

### Type System
- Extract type hints from Python
- Use structs for classes/objects
- Use interfaces for abstraction (similar to protocols)
- No generics unless Go 1.18+
- Zero values are explicit (not implicit like Python)

### Naming & Style
- Exported symbols (public): CamelCase
- Private symbols: camelCase
- Package names: lowercase, no underscores
- Interfaces: end with 'er' suffix when appropriate
- Follow [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)

### Standard Library
- `net/http` for HTTP
- `crypto/*` for cryptography (no external crypto libs unless necessary)
- `encoding/json` for JSON (no reflection-based libs)
- `database/sql` for database access
- `context` for cancellation + deadlines

### External Dependencies
- Only use mature, well-maintained packages
- Prefer stdlib alternatives when available
- Always specify semantic versions in go.mod
- Keep dependency count minimal

### Specific Patterns

#### Python asyncio → GO goroutines
```python
# Python
async def fetch_data(url):
    result = await http.get(url)
    return result

# GO
func fetchData(ctx context.Context, url string) (string, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return "", fmt.Errorf("fetch failed: %w", err)
    }
    // ...
}
```

#### Python classes → GO structs + methods
```python
# Python
class Agent:
    def __init__(self, hostname: str):
        self.hostname = hostname

    def register(self):
        # ...

# GO
type Agent struct {
    Hostname string
}

func (a *Agent) Register(ctx context.Context) error {
    // ...
    return nil
}
```

#### Python dict/JSON → GO structs with tags
```python
# Python
data = {"hostname": "host-01", "status": "connected"}

# GO
type AgentInfo struct {
    Hostname string `json:"hostname"`
    Status   string `json:"status"`
}
```

### Code Quality
- Always handle errors explicitly
- Use `defer` for cleanup (like Python context managers)
- Keep functions small and focused
- Use helper functions/packages to reduce duplication
- Add comments for non-obvious logic

### Import Organization
Group imports in order:
1. Standard library
2. External packages
3. Internal packages

Example:
```go
import (
    "context"
    "crypto/rsa"
    "encoding/json"
    "fmt"

    "github.com/nats-io/nats.go"
    "github.com/gorilla/websocket"

    "ansiblerelay/pkg/storage"
    "ansiblerelay/pkg/broker"
)
```

## Output Requirements

1. Return ONLY valid GO code (no explanations in code)
2. Include package declaration at top
3. Include necessary imports
4. Add comments explaining key differences from Python
5. Use `gofmt` style (run `goimports -w` after generation)
6. Ensure code compiles (`go build` should succeed)

## Error Cases

If the Python code has issues:
- Ambiguous types: use `interface{}` or ask user to add type hints
- Unclear behavior: add comments with questions/assumptions
- Missing dependencies: suggest GO alternative or note external dependency

Return complete, compilable GO code that maintains the same behavior as Python.
"""


def read_python_file(path: str) -> str:
    """Read Python source file."""
    with open(path, 'r') as f:
        return f.read()


def convert_to_go(python_code: str) -> str:
    """Convert Python code to GO using Claude API."""
    client = anthropic.Anthropic()

    message = client.messages.create(
        model="claude-opus-4-6",
        max_tokens=8000,
        system=SYSTEM_PROMPT,
        messages=[
            {
                "role": "user",
                "content": f"Convert this Python code to idiomatic GO:\n\n```python\n{python_code}\n```"
            }
        ]
    )

    return message.content[0].text


def write_go_file(path: str, content: str) -> None:
    """Write GO code to file."""
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, 'w') as f:
        f.write(content)
    print(f"✅ Wrote: {path}")


def main():
    parser = argparse.ArgumentParser(
        description="Convert Python code to GO using Claude API",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python3 convert_python_to_go.py \\
    --input server/api/routes_register.py \\
    --output server/pkg/handlers/register.go

  python3 convert_python_to_go.py \\
    --input server/db/agent_store.py \\
    --output server/pkg/storage/agent_store.go
        """
    )

    parser.add_argument('--input', required=True, help='Input Python file')
    parser.add_argument('--output', required=True, help='Output GO file')
    parser.add_argument('--api-key', help='ANTHROPIC_API_KEY (or use env var)')

    args = parser.parse_args()

    # Validate input
    if not os.path.exists(args.input):
        print(f"ERROR: Input file not found: {args.input}", file=sys.stderr)
        sys.exit(1)

    # Read Python source
    print(f"📖 Reading: {args.input}")
    python_code = read_python_file(args.input)
    print(f"   Lines: {len(python_code.splitlines())}")

    # Convert
    print(f"🔄 Converting to GO...")
    try:
        go_code = convert_to_go(python_code)
    except anthropic.APIError as e:
        print(f"ERROR: API call failed: {e}", file=sys.stderr)
        sys.exit(1)

    # Write output
    write_go_file(args.output, go_code)

    # Summary
    print(f"\n📊 Summary:")
    print(f"   Input:  {args.input} ({len(python_code)} bytes)")
    print(f"   Output: {args.output} ({len(go_code)} bytes)")
    print(f"\n✨ Next steps:")
    print(f"   1. Review: {args.output}")
    print(f"   2. Format: goimports -w {args.output}")
    print(f"   3. Test: go build && go test")


if __name__ == "__main__":
    main()
