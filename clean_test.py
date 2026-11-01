with open('internal/warp/penalty_test.go', 'r', encoding='utf-8') as f:
    lines = f.readlines()

new_lines = []
for line in lines:
    # Skip line 24 (strikes map init)
    if line.strip() == 'strikes:   make(map[string]int),':
        continue
    # Skip line 47 (strikes removed comment)
    if line.strip() == '// strikes removed: fixed 24h blacklist':
        continue
    # Skip line 71 (strikes int in struct)
    if line.strip() == 'strikes:   make(map[string]int),' or 'strikes int' in line:
        continue
    # Skip lines 81-82 (strikes usage in test case)
    if 'test.strikes' in line:
        continue
    # Skip line 129 (pool.strikes[key] = 3)
    if 'pool.strikes[key]' in line:
        continue
    # Skip line 137 (strikes round trip)
    if 'strikes round trip' in line:
        continue
    # Skip line 144 (strikes removed comment in new test)
    if 'strikes removed: fixed 24h blacklist, nothing to clear' in line:
        continue
    new_lines.append(line)

with open('internal/warp/penalty_test.go', 'w', encoding='utf-8') as f:
    f.writelines(new_lines)

print('test file cleaned')