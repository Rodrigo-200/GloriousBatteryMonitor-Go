import re
p = re.compile(r'^\s*func\s+')
with open('d:/GloriousBatteryMonitor-clean/main.go','r',encoding='utf-8') as f:
    for i,line in enumerate(f, start=1):
        if p.search(line):
            print(i, line.rstrip())
