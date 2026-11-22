BEGIN {
    FS = ":.*##[[:space:]]*"
}

/^[[:alnum:]_.-]+:.*##/ {
    target = $1
    description = $2
    printf "%-24s %s\n", target, description
}
