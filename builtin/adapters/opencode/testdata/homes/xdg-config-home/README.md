# xdg-config-home

XDG_CONFIG_HOME points at `~/dotfiles/config`, so OpenCode's configuration
root is `~/dotfiles/config/opencode` (`opencode debug paths` on 2.4.1 shows
this; without the variable it is `~/.config/opencode`, the other cases).
Everything goes there. A stale `~/.config/opencode` from before the move is
left exactly as it is.
