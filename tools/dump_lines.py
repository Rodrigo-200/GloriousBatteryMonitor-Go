import sys
path = r"d:\\GloriousBatteryMonitor-clean\\main.go"
with open(path, 'rb') as f:
    data = f.read()
lines = data.splitlines(True)
for i in range(2440, 2450):
    idx = i-1
    if 0 <= idx < len(lines):
        print(i, repr(lines[idx]))
    else:
        print(i, '<no line>')
