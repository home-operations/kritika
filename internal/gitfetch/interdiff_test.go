package gitfetch

import "testing"

const twoHunks = `diff --git a/svc.go b/svc.go
index 1111111..2222222 100644
--- a/svc.go
+++ b/svc.go
@@ -1,3 +1,4 @@
 package svc
+// svc is the service.
 
 func one() {}
@@ -9,3 +10,5 @@ func four() {}
 
 func five() {}
+
+func c() {}
`

const baseEdit = `diff --git a/svc.go b/svc.go
index 3333333..4444444 100644
--- a/svc.go
+++ b/svc.go
@@ -1,3 +1,4 @@
 package svc
+// svc is the service.   
 
 func one() {}
`

func TestWithoutHunks(t *testing.T) {
	if gained := hunkKeys(baseEdit); len(gained) != 1 {
		t.Fatalf("hunk keys = %v", gained)
	}
	tests := []struct {
		name string
		diff string
		want string
	}{
		{name: "the base's hunk goes, the change's stays, whatever the positions and trailing whitespace",
			diff: twoHunks, want: "diff --git a/svc.go b/svc.go\nindex 1111111..2222222 100644\n--- a/svc.go\n+++ b/svc.go\n" +
				"@@ -9,3 +10,5 @@ func four() {}\n \n func five() {}\n+\n+func c() {}\n"},
		{name: "a file left with no hunk is left out", diff: baseEdit, want: ""},
		{name: "the change making the base's edit elsewhere keeps it, once the base's goes",
			diff: "diff --git a/svc.go b/svc.go\n--- a/svc.go\n+++ b/svc.go\n@@ -1,3 +1,4 @@\n package svc\n+// svc is the service.\n \n func one() {}\n" +
				"@@ -9,3 +10,4 @@ func four() {}\n \n+// svc is the service.\n func five() {}\n",
			want: "diff --git a/svc.go b/svc.go\n--- a/svc.go\n+++ b/svc.go\n@@ -9,3 +10,4 @@ func four() {}\n \n+// svc is the service.\n func five() {}\n"},
		{name: "a rename alone stands", diff: "diff --git a/svc.go b/lib.go\nsimilarity index 100%\nrename from svc.go\nrename to lib.go\n",
			want: "diff --git a/svc.go b/lib.go\nsimilarity index 100%\nrename from svc.go\nrename to lib.go\n"},
		{name: "the same lines in another file stay", diff: "diff --git a/other.go b/other.go\n--- a/other.go\n+++ b/other.go\n@@ -1,2 +1,3 @@\n package svc\n+// svc is the service.\n \n",
			want: "diff --git a/other.go b/other.go\n--- a/other.go\n+++ b/other.go\n@@ -1,2 +1,3 @@\n package svc\n+// svc is the service.\n \n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withoutHunks(tt.diff, hunkKeys(baseEdit)); got != tt.want {
				t.Errorf("withoutHunks() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestParseFileDiffNamesADeletion(t *testing.T) {
	path, header, hunks := parseFileDiff("diff --git a/gone.go b/gone.go\ndeleted file mode 100644\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-package gone\n")
	if path != "gone.go" || header != "diff --git a/gone.go b/gone.go\ndeleted file mode 100644\n--- a/gone.go\n+++ /dev/null\n" || len(hunks) != 1 {
		t.Fatalf("parseFileDiff() = %q, %q, %q", path, header, hunks)
	}
}
