import sys

path = r"d:\\GloriousBatteryMonitor-clean\\main.go"

with open(path, 'r', encoding='utf-8') as f:
    data = f.read()

stack = []
line = 1
col = 0
i = 0
n = len(data)

in_line_comment = False
in_block_comment = False
in_string = False  # double-quoted
in_raw_string = False  # backtick
in_char = False
escape = False

while i < n:
    c = data[i]
    col += 1

    # handle newline
    if c == '\n':
        line += 1
        col = 0
        if in_line_comment:
            in_line_comment = False
        i += 1
        continue

    if in_line_comment:
        i += 1
        continue
    if in_block_comment:
        if c == '*' and i+1 < n and data[i+1] == '/':
            in_block_comment = False
            i += 2
            col += 1
            continue
        i += 1
        continue
    if in_string:
        if escape:
            escape = False
        elif c == '\\':
            escape = True
        elif c == '"':
            in_string = False
        i += 1
        continue
    if in_raw_string:
        if c == '`':
            in_raw_string = False
        i += 1
        continue
    if in_char:
        if escape:
            escape = False
        elif c == '\\':
            escape = True
        elif c == "'":
            in_char = False
        i += 1
        continue

    # Not in any special state
    if c == '/' and i+1 < n and data[i+1] == '/':
        in_line_comment = True
        i += 2
        col += 1
        continue
    if c == '/' and i+1 < n and data[i+1] == '*':
        in_block_comment = True
        i += 2
        col += 1
        continue
    if c == '"':
        in_string = True
        i += 1
        continue
    if c == '`':
        in_raw_string = True
        i += 1
        continue
    if c == "'":
        in_char = True
        i += 1
        continue

    if c == '(':
        stack.append((line, col, i))
    elif c == ')':
        if stack:
            stack.pop()
        else:
            print(f"Unmatched ')' at line {line} col {col}")
    i += 1

if stack:
    print(f"UNMATCHED '(' COUNT = {len(stack)}")
    for (ln, cl, idx) in stack:
        # Print the line
        start = data.rfind('\n', 0, idx)
        if start == -1:
            start = 0
        else:
            start += 1
        end = data.find('\n', idx)
        if end == -1:
            end = len(data)
        line_text = data[start:end]
        print(f"Line {ln} Col {cl}: {line_text.strip()}")
else:
    print("All parentheses matched")
