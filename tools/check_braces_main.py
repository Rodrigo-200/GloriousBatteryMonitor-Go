import sys, re
path = r"d:\\GloriousBatteryMonitor-clean\\main.go"

def strip_strings_and_comments(s):
    s = re.sub(r'//.*', '', s)
    s = re.sub(r'/\*.*?\*/', '', s)
    s = re.sub(r'"(?:\\.|[^"\\])*"', '""', s)
    s = re.sub(r"'(?:\\.|[^'\\])+'", "''", s)
    return s

try:
    with open(path, 'r', encoding='utf-8') as f:
        depth = 0
        paren = 0
        bracket = 0
        in_block_comment = False
        for i, raw_line in enumerate(f, start=1):
            line = raw_line
            if in_block_comment:
                end_idx = line.find('*/')
                if end_idx >= 0:
                    line = line[end_idx+2:]
                    in_block_comment = False
                else:
                    continue
            while True:
                start_idx = line.find('/*')
                if start_idx >= 0:
                    end_idx = line.find('*/', start_idx+2)
                    if end_idx >= 0:
                        line = line[:start_idx] + line[end_idx+2:]
                        continue
                    else:
                        line = line[:start_idx]
                        in_block_comment = True
                        break
                break

            s = strip_strings_and_comments(line)
            depth += s.count('{') - s.count('}')
            paren += s.count('(') - s.count(')')
            bracket += s.count('[') - s.count(']')
            if depth < 0:
                print(f'BRACE_NEGATIVE at line {i}')
                sys.exit(0)
            if paren < 0:
                print(f'PAREN_NEGATIVE at line {i}')
                sys.exit(0)
            if bracket < 0:
                print(f'BRACKET_NEGATIVE at line {i}')
                sys.exit(0)
        print(f'BRACE_FINAL_DEPTH={depth}')
        print(f'PAREN_FINAL_DEPTH={paren}')
        print(f'BRACKET_FINAL_DEPTH={bracket}')
except Exception as e:
    print('ERROR', e)
    sys.exit(1)
