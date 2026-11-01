f = open('internal/warp/penalty_test.go', encoding='utf-8', errors='ignore')
data = f.read()
f.close()
# 1. Remove strikes initialization from testPool
old = '        strikes:   make(map[string]int),\n'
new = '        // strikes removed (fixed 24h blacklist)\n'
data = data.replace(old, new)
# 2. Fix expected cooldown in TestQuotaPenaltyIsNotRestored (line 44)
data = data.replace('time.Now().Add(25*time.Minute)', 'time.Now().Add(24*time.Hour)')
# 3. Remove strikes check in TestQuotaPenaltyIsNotRestored (lines 47-49)
old2 = '\tif got := pool.strikes[blocked.egressKey()]; got != 1 {\n\t\tt.Fatalf("expected one quota strike, got %d", got)\n\t}\n'
data = data.replace(old2, '\t// strikes removed: fixed 24h blacklist\n')
# 4. Update test cases in TestQuotaCooldownEscalates: fixed 24h
old3 = '{1, 30 * time.Minute},\n\t\t{2, 2 * time.Hour},\n\t\t{3, 8 * time.Hour},\n\t\t{4, 24 * time.Hour},\n\t\t{8, 24 * time.Hour},'
new3 = '{1, 24 * time.Hour},\n\t\t{2, 24 * time.Hour},\n\t\t{3, 24 * time.Hour},\n\t\t{4, 24 * time.Hour},\n\t\t{8, 24 * time.Hour},'
data = data.replace(old3, new3)
# 5. Fix TestPenaltyStateRoundTrip: remove strikes check
old4 = '\tif got := strikes[key]; got != 3 {\n\t\tt.Fatalf("strikes round trip = %d want 3", got)\n\t}\n'
new4 = '\t// strikes removed: cooling only persists\n'
data = data.replace(old4, new4)
# 6. Update TestSuccessClearsStrikes (remove strikes reference, keep test working)
old5 = 'func TestSuccessClearsStrikes(t *testing.T) {\n\tfirst := testExit()\n\tpool := testPool("", first)\n\tkey := first.egressKey()\n\tpool.strikes[key] = 2\n\tpool.noteSuccess(first)\n\tif _, exists := pool.strikes[key]; exists {\n\t\tt.Fatal("success did not clear the quota strike")\n\t}\n}\n'
new5 = 'func TestNoteSuccessNoStrikes(t *testing.T) {\n\tfirst := testExit()\n\tpool := testPool("", first)\n\tkey := first.egressKey()\n\t// strikes removed: fixed 24h blacklist, nothing to clear\n\tpool.noteSuccess(first)\n}\n'
data = data.replace(old5, new5)
# 7. Fix loadPenalties call: strikes return value ignored
old6 = '\tcooling, strikes := loadPenalties(path)\n'
new6 = '\tcooling, _ := loadPenalties(path)\n'
data = data.replace(old6, new6)
# 8. Remove strikes from testPool definition
f2 = open('internal/warp/penalty_test.go', 'w', encoding='utf-8')
f2.write(data)
f2.close()
print('penalty_test updated')
