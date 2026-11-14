package scoring

import (
	"fmt"
	"strings"
)

func stars(v *int) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%d/5", *v)
}

func fnum(v *float64) string {
	if v == nil {
		return "[FALTA]"
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", *v), "0"), ".")
}

// Markdown renderiza el informe equivalente al informe.md de interview-sim.
func Markdown(r *Report) string {
	var o []string
	add := func(s ...string) { o = append(o, s...) }
	add("# Informe de práctica: "+r.ScenarioTitle, "")
	add(fmt.Sprintf("- Fecha: %s", r.StartedAt.UTC().Format("2006-01-02 15:04 UTC")))
	add(fmt.Sprintf("- Idioma: %s · Modo: %s · Entrevistador (LLM): %s", r.Lang, r.Mode, r.LLM))
	ended := ""
	if r.EndedByUser {
		ended = " (terminada antes por vos)"
	}
	add(fmt.Sprintf("- Preguntas respondidas: %d%s", r.QuestionsAnswered, ended), "")
	add("> "+r.Disclaimer, "")
	add("## Las 3 cosas a practicar", "")
	for _, t := range r.Top3 {
		label := DimLabel[t.Dimension]
		if label == "" {
			label = t.Dimension
		}
		add(fmt.Sprintf("%d. **%s** (promedio %s/5): %s", t.Rank, label, fnum(t.Mean), t.Advice))
	}
	add("", "## Resumen por criterio", "", "| Criterio | Promedio |", "|---|---|")
	means := map[string]*float64{"claridad": r.DimMeans.Claridad, "estructura": r.DimMeans.Estructura, "concrecion": r.DimMeans.Concrecion,
		"duracion": r.DimMeans.Duracion, "muletillas": r.DimMeans.Muletillas, "relevancia": r.DimMeans.Relevancia}
	for _, d := range Dims {
		add(fmt.Sprintf("| %s | %s |", DimLabel[d], fnum(means[d])))
	}
	add("", "## Respuesta por respuesta")
	for n, b := range r.Blocks {
		add("", fmt.Sprintf("### %d. %s", n+1, b.Question))
		for _, s := range b.Turns {
			label := "Pregunta"
			if s.Kind == "followup" && s.Probe != nil {
				label = "Repregunta (" + *s.Probe + ")"
			}
			add("", fmt.Sprintf("**%s:** %s", label, s.Question), "", "**Tu respuesta:** "+s.Answer, "")
			var parts []string
			for _, d := range Dims {
				parts = append(parts, DimLabel[d]+" "+stars(s.Dims.Get(d)))
			}
			add(fmt.Sprintf("- Puntaje: **%s/5** · %s", fnum(s.Overall), strings.Join(parts, " · ")))
			est := ""
			if s.Details.SegundosEstimados {
				est = " (estimado por palabras)"
			}
			add(fmt.Sprintf("- Duración: ~%.1f s%s (ideal %.0f-%.0f s) · %d palabras", s.Details.Segundos, est, s.Details.IdealSegundos[0], s.Details.IdealSegundos[1], s.Details.Palabras))
			mul := strings.Join(s.Details.Muletillas, ", ")
			if mul == "" {
				mul = "ninguna"
			}
			add(fmt.Sprintf("- Muletillas: %s (%.1f cada 100 palabras; fuente: %s)", mul, s.Details.MuletillasCada100, s.Details.MuletillasFuente))
			if p := s.Details.Pausas; p != nil {
				add(fmt.Sprintf("- Pausas largas: %d (total %.1f s, la más larga %.1f s)", p.Count, p.TotalS, p.LongestS))
			}
			nums := strings.Join(s.Details.Numeros, ", ")
			if nums == "" {
				nums = "ninguno"
			}
			add("- Números/magnitudes: " + nums)
			st := s.Details.Star
			mark := func(b bool) string {
				if b {
					return "✓"
				}
				return "✗"
			}
			add(fmt.Sprintf("- STAR detectado: S%s T%s A%s R%s", mark(st.S), mark(st.T), mark(st.A), mark(st.R)))
			add("- Comentario: " + s.Comment)
			if len(s.LowConfidence) > 0 {
				add("- Palabras que el STT entendió con poca seguridad: " + strings.Join(s.LowConfidence, ", "))
			}
		}
		add("", fmt.Sprintf("**Versión mejorada** (fuente: %s; solo con lo que dijiste):", b.Improved.Source), "")
		var lines []string
		for _, ln := range strings.Split(b.Improved.Text, "\n") {
			if strings.TrimSpace(ln) != "" {
				lines = append(lines, "> "+ln)
			}
		}
		add(strings.Join(lines, "\n>\n"))
		if len(b.Improved.Missing) > 0 {
			add("", "Datos que faltaron y te conviene tener listos: "+strings.Join(b.Improved.Missing, "; "))
		}
		if len(b.Improved.RejectedTerms) > 0 {
			add("", "Se descartó una mejora del LLM porque agregaba datos que no dijiste: `"+strings.Join(b.Improved.RejectedTerms, "`, `")+"`")
		}
		if e := b.English; e != nil {
			add("", "**English feedback** (a partir de la transcripción, no del audio):")
			for _, g := range e.Grammar {
				add(fmt.Sprintf("- \"%s\" → \"%s\": %s", g.Original, g.Corrected, g.Explanation))
			}
			for _, t := range e.Tips {
				add("- Tip: " + t)
			}
			if len(e.LowConfidence) > 0 {
				add("- Posible pronunciación poco clara: " + strings.Join(e.LowConfidence, ", ") + ". Repetí estas palabras lento.")
			}
		}
	}
	add("", "## Transcripción completa", "")
	for _, b := range r.Blocks {
		for _, s := range b.Turns {
			add("**Entrevistadora:** "+s.Question, "", "**Vos:** "+s.Answer, "")
		}
	}
	if len(r.Faltantes) > 0 {
		add("## Datos faltantes", "")
		for _, f := range r.Faltantes {
			add("- " + f)
		}
		add("")
	}
	if len(r.LLMErrors) > 0 {
		add("## Avisos técnicos", "", fmt.Sprintf("El LLM falló %d vez/veces y se usó la lógica local. Primer error: %s", len(r.LLMErrors), trunc(r.LLMErrors[0], 200)), "")
	}
	return strings.TrimRight(strings.Join(o, "\n"), "\n") + "\n"
}

func trunc(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
