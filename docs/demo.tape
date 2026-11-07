# Records docs/img/demo.gif. Run from the repo root with the outflow
# binary on PATH, e.g.:
#   go build -o /tmp/outflow-bin/outflow .
#   PATH=/tmp/outflow-bin:$PATH vhs docs/demo.tape
Output docs/img/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1400
Set Height 800
Set Padding 12
Set Framerate 15
Set TypingSpeed 40ms

Type "clear" Enter
Sleep 300ms
Type "outflow --demo --demo-rule"
Sleep 600ms
Enter
Sleep 2500ms

Down
Sleep 1200ms
Enter
Sleep 2000ms
Down
Sleep 1500ms
Type "e"
Sleep 3000ms
Escape
Sleep 1000ms
Type "f"
Sleep 2000ms
Type "?"
Sleep 3000ms
Type "q"
Sleep 800ms
