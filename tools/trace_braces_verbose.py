import re
path = r"d:\\GloriousBatteryMonitor-clean\\main.go"

with open(path, 'r', encoding='utf-8') as f:
    data = f.read()

stack = []
line = 1
col = 0
in_block_comment = False
in_string = False
in_raw_string = False
in_char = False
escape = False

for i, ch in enumerate(data):
    if ch == '\n':
        line += 1
        col = 0
        if in_block_comment and data[i-1] == '*' and data[i] == '/':
            in_block_comment = False
        continue
    col += 1
    # naive skip of comments and strings
    if in_block_comment:
        if ch == '*' and i+1 < len(data) and data[i+1] == '/':
            in_block_comment = False
        continue
    if in_string:
        if escape:
            escape = False
        elif ch == '\\':
            escape = True
        elif ch == '"':
            in_string = False
        continue
    if in_raw_string:
        if ch == '`':
            in_raw_string = False
        continue
    if in_char:
        if escape:
            escape = False
        elif ch == '\\':
            escape = True
        elif ch == "'":
            in_char = False
        continue

    if ch == '/' and i+1 < len(data) and data[i+1] == '/':
        # skip until end of line
        j = data.find('\n', i)
        if j == -1:
            break
        line += data.count('\n', i, j)
        # advance pointer
        continue
    if ch == '/' and i+1 < len(data) and data[i+1] == '*':
        in_block_comment = True
        continue
    if ch == '"':
        in_string = True
        continue
    if ch == '`':
        in_raw_string = True
        continue
    if ch == "'":
        in_char = True
        continue

    if ch == '{':
        stack.append((line, col))
        print(f'PUSH {{ at line {line} col {col} (stack depth {len(stack)})')
    elif ch == '}':
        if stack:
            ln, cl = stack.pop()
            print(f'POP  }} at line {line} col {col} (matched push at line {ln} col {cl})')
        else:
            print(f'POP  }} at line {line} col {col} (NO MATCH)')

if stack:
    print('UNMATCHED PUSHES:')
    for ln, cl in stack:
        print(f'  unmatched {{ at line {ln} col {cl}')
else:
    print('ALL BRACES MATCHED')
