import re, sys
path = r"d:\\GloriousBatteryMonitor-clean\\driver.go"

def strip_strings_and_comments(s):
    s = re.sub(r'//.*', '', s)
    s = re.sub(r'/\*.*?\*/', '', s)
    s = re.sub(r'"(?:\\.|[^"\\])*"', '""', s)
    s = re.sub(r"'(?:\\.|[^'\\])+'", "''", s)
    return s


def find_matching_close(start_line):
    with open(path, 'r', encoding='utf-8') as f:
        lines = f.readlines()
    depth = 0
    in_block = False
    for i in range(start_line-1, len(lines)):
        line = lines[i]
        # handle block comment
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
                depth += 1
            elif ch == '}':
                depth -= 1
                if depth == 0:
                    return i+1
    return None

for ln in (1512, 1667):
    match = find_matching_close(ln)
    print(f'Open at {ln} -> matching close at {match}')
