import re

path = r"d:\\GloriousBatteryMonitor-clean\\driver.go"
start = 1400
end = 1700

def strip_strings_and_comments(s):
    s = re.sub(r'//.*', '', s)
    s = re.sub(r'/\*.*?\*/', '', s)
    s = re.sub(r'"(?:\\.|[^"\\])*"', '""', s)
    s = re.sub(r"'(?:\\.|[^'\\])+'", "''", s)
    return s

with open(path, 'r', encoding='utf-8') as f:
    stack = []
    in_block = False
    for i, raw in enumerate(f, start=1):
        line = raw
        if i < start or i > end:
            # still advance stack but without printing
            if in_block:
                end_idx = line.find('*/')
                if end_idx >= 0:
                    line = line[end_idx+2:]
                    in_block = False
                else:
                    continue
            while True:
                st = line.find('/*')
                if st >= 0:
                    ed = line.find('*/', st+2)
                    if ed >= 0:
                        line = line[:st] + line[ed+2:]
                        continue
                    else:
                        line = line[:st]
                        in_block = True
                        break
                break
            s = strip_strings_and_comments(line)
            for ch in s:
                if ch == '{':
                    stack.append((i, line))
                elif ch == '}':
                    if stack:
                        stack.pop()
                    else:
                        print('UNEXPECTED_CLOSING at', i)
            continue
        # within printed range
        if in_block:
            end_idx = line.find('*/')
            if end_idx >= 0:
                line = line[end_idx+2:]
                in_block = False
            else:
                continue
        # strip block comments safely
        while True:
            st = line.find('/*')
            if st >= 0:
                ed = line.find('*/', st+2)
                if ed >= 0:
                    line = line[:st] + line[ed+2:]
                    continue
                else:
                    line = line[:st]
                    in_block = True
                    break
            break
        s = strip_strings_and_comments(line)
        for idx, ch in enumerate(s):
            if ch == '{':
                stack.append((i, idx))
                print(f'PUSH at line {i} col {idx}: {raw.strip()}')
            elif ch == '}':
                if stack:
                    ln, col = stack.pop()
                    print(f'POP at line {i} col {idx}: matching open at line {ln} col {col}')
                else:
                    print(f'UNEXPECTED CLOSE at line {i} col {idx}')
    print('\nRemaining stack (unmatched opens):')
    for ln, col in stack:
        print('  open at line', ln, 'col', col)
