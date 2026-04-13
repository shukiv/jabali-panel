# Bash completion for jabali-terminal.
_jabali_terminal() {
    local cur prev words cword
    _init_completion || return

    local top="status logs sessions version help"

    if [ "$cword" -eq 1 ]; then
        COMPREPLY=($(compgen -W "$top" -- "$cur"))
        return
    fi

    case "${words[1]}" in
        logs)
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "-f" -- "$cur"))
            fi
            ;;
        sessions)
            if [ "$cword" -eq 2 ]; then
                COMPREPLY=($(compgen -W "list show" -- "$cur"))
            elif [ "$cword" -eq 3 ] && [ "${words[2]}" = "show" ]; then
                local ids
                ids=$(ls /var/log/jabali-terminal/sessions/*.log 2>/dev/null \
                      | xargs -n1 basename 2>/dev/null \
                      | sed 's/\.log$//')
                COMPREPLY=($(compgen -W "$ids" -- "$cur"))
            fi
            ;;
    esac
}

complete -F _jabali_terminal jabali-terminal
