/^Libs:/ { print $0 " -lpcre2-8 -lm"; next }
{ print }
