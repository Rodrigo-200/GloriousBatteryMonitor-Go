import re
path = r"d:\\GloriousBatteryMonitor-clean\\main.go"

with open(path, 'r', encoding='utf-8') as f:
    depth = 0
    in_block = False
    for i, raw in enumerate(f, start=1):
        line = raw
        # strip block comments
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
        s = re.sub(r'//.*', '', line)
        opens = s.count('{')
        closes = s.count('}')
        if opens or closes:
            depth += opens - closes
            print(f'Line {i}: opens={opens} closes={closes} depth={depth} -> {raw.strip()}')
        if depth < 0:
            print('NEGATIVE at', i)
            break
        if i >= 420:
            break
    print('FINAL depth at prefix=', depth)
