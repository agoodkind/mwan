BEGIN {
    names[1] = "mwan-gomod-" suffix
    names[2] = "mwan-build-" suffix
    names[3] = "mwan-gomk-" suffix
    limits[names[1]] = gomod_limit
    limits[names[2]] = build_limit
    limits[names[3]] = gomk_limit

    megabytes["B"] = 0.000001
    megabytes["kB"] = 0.001
    megabytes["MB"] = 1
    megabytes["GB"] = 1000
    megabytes["TB"] = 1000000
}

$1 in limits {
    users[$1] = $2
    sizes[$1] = $3
}

END {
    for (volume_index = 1; volume_index <= 3; volume_index++) {
        name = names[volume_index]
        limit = limits[name]
        if (!(name in sizes)) {
            print name, "-", "-", limit, "absent"
            continue
        }
        action = "keep"
        size = sizes[name]
        user_count = users[name]
        if (user_count !~ /^[0-9]+$/) {
            printf "Docker did not report a container count for %s.\n", name > "/dev/stderr"
            exit 1
        }
        unit = size
        sub(/^[0-9]+(\.[0-9]+)?/, "", unit)
        if (mode == "cache-trim" && !(unit in megabytes)) {
            printf "Docker reported an unknown volume size for %s: %s.\n", name, size > "/dev/stderr"
            exit 1
        }
        if (user_count > 0) {
            action = "in-use"
        } else if (mode == "cache-clean" || (mode == "cache-trim" && size * megabytes[unit] > limit)) {
            action = "delete"
        }
        print name, size, user_count, limit, action
    }
}
