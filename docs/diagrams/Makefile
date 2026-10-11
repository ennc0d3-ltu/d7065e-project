# Render every D2 source to SVG (for viewing) and PDF (for the LaTeX report),
# with the course's theme, layout and padding. `make` re-renders only what changed.
D2FLAGS := --theme 4 --layout elk --pad 25
SRC := $(wildcard *.d2)

all: $(SRC:.d2=.svg) $(SRC:.d2=.pdf)

# the overview reads best with dagre (the course notes: good for label-dense diagrams)
overview.svg: overview.d2
	d2 --theme 4 --layout dagre --pad 25 $< $@

%.svg: %.d2
	d2 $(D2FLAGS) $< $@

%.pdf: %.svg
	rsvg-convert -f pdf -o $@ $<

clean:
	rm -f *.svg *.pdf

.PHONY: all clean
