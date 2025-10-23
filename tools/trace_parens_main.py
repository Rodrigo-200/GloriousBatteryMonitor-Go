import re
path = r"d:\\GloriousBatteryMonitor-clean\\main.go"

def strip_strings_and_comments(s):
    s = re.sub(r'//.*', '', s)
    s = re.sub(r'/\*.*?\*/', '', s)
    s = re.sub(r'"(?:\\.|[^"\\])*"', '""', s)
    s = re.sub(r"'(?:\\.|[^'\\])+'", "''", s)
    return s

with open(path, 'r', encoding='utf-8') as f:
    depth = 0
    for i, raw in enumerate(f, start=1):
        line = raw
        # Remove block comments naive
        while True:
            st = line.find('/*')
            if st >= 0:
                ed = line.find('*/', st+2)
                if ed >= 0:
                    line = line[:st] + line[ed+2:]
                    continue
                else:
                    line = line[:st]
                    break
            break
        s = strip_strings_and_comments(line)
        opens = s.count('(')
        closes = s.count(')')
        if opens or closes:
            depth += opens - closes
            print(f'Line {i}: opens={opens} closes={closes} depth={depth} -> {raw.strip()}')
    print('FINAL_PAREN_DEPTH=', depth)
