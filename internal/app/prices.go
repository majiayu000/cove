package app

import (
	"database/sql"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"time"
)

const PriceSchema = `CREATE TABLE IF NOT EXISTS prices(id TEXT PRIMARY KEY,model_id TEXT NOT NULL REFERENCES source_models(id),effective_at TEXT NOT NULL,data TEXT NOT NULL,UNIQUE(model_id,effective_at));`

type PriceUnit struct {
	Dimension string `json:"dimension"`
	Amount    string `json:"amount"`
	Per       string `json:"per"`
}
type PriceVersion struct {
	ID          string      `json:"id"`
	ModelID     string      `json:"model_id"`
	Currency    string      `json:"currency"`
	EffectiveAt time.Time   `json:"effective_at"`
	Units       []PriceUnit `json:"units"`
	Provenance  struct {
		Kind       string    `json:"kind"`
		URL        string    `json:"url,omitempty"`
		ObservedAt time.Time `json:"observed_at"`
	} `json:"provenance"`
}

func validPriceUnits(units []PriceUnit) bool {
	if len(units) == 0 || len(units) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, u := range units {
		amount, ok := new(big.Rat).SetString(u.Amount)
		per, pok := new(big.Rat).SetString(u.Per)
		if u.Dimension == "" || len(u.Dimension) > 80 || seen[u.Dimension] || !ok || !pok || amount.Sign() < 0 || per.Sign() <= 0 || !decimalPattern.MatchString(u.Amount) || !decimalPattern.MatchString(u.Per) {
			return false
		}
		seen[u.Dimension] = true
	}
	return true
}
func (a *App) pricesAPI(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == "GET" {
		rows, e := a.Store.DB.Query("SELECT data FROM prices ORDER BY effective_at DESC,id")
		if e != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		items := []PriceVersion{}
		for rows.Next() {
			var raw string
			var v PriceVersion
			if e = rows.Scan(&raw); e != nil {
				break
			}
			if e = json.Unmarshal([]byte(raw), &v); e != nil {
				break
			}
			items = append(items, v)
		}
		e2 := rows.Close()
		if e != nil || rows.Err() != nil || e2 != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nil})
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "价格版本不可修改，创建新版本", "")
		return
	}
	var v PriceVersion
	if !decode(w, r, &v) {
		return
	}
	if v.ID != "" || v.EffectiveAt.IsZero() || !currencyPattern.MatchString(v.Currency) || !validPriceUnits(v.Units) || v.Provenance.Kind == "" {
		fail(w, 400, "需模型、币种、生效时间、正数per与价格来源", "units")
		return
	}
	if v.Provenance.URL != "" && validateURL(v.Provenance.URL) != nil {
		fail(w, 400, "价格来源URL无效", "provenance.url")
		return
	}
	m, e := a.Store.model(v.ModelID)
	if e == sql.ErrNoRows {
		fail(w, 404, "模型不存在", "model_id")
		return
	}
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	v.ID = id("price")
	v.EffectiveAt = v.EffectiveAt.UTC()
	tx, e := a.Store.DB.Begin()
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	var exists int
	e = tx.QueryRow("SELECT count(*) FROM prices WHERE model_id=? AND effective_at=?", v.ModelID, v.EffectiveAt.Format(time.RFC3339Nano)).Scan(&exists)
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if exists > 0 {
		fail(w, 409, "同模型同生效时间已有价格版本", "effective_at")
		return
	}
	_, e = tx.Exec("INSERT INTO prices(id,model_id,effective_at,data) VALUES(?,?,?,?)", v.ID, v.ModelID, v.EffectiveAt.Format(time.RFC3339Nano), encode(v))
	if e == nil && !v.EffectiveAt.After(time.Now()) {
		p := v.snapshot()
		m.Price = &p
		m.Version++
		_, e = tx.Exec("UPDATE source_models SET data=? WHERE id=?", encode(m), m.ID)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	writeJSON(w, 201, v)
}
func (v PriceVersion) snapshot() Price {
	p := Price{ID: v.ID, Currency: v.Currency, AsOf: v.EffectiveAt.Format(time.RFC3339Nano), Units: v.Units}
	p.Input = priceTokenRate(&p, "input_token")
	p.Output = priceTokenRate(&p, "output_token")
	p.Cached = priceTokenRate(&p, "cached_input_token")
	p.CacheCreation = priceTokenRate(&p, "cache_creation_token")
	return p
}
func (s *Store) effectivePrice(mid string, fallback *Price, at time.Time) (*Price, error) {
	var raw string
	e := s.DB.QueryRow("SELECT data FROM prices WHERE model_id=? AND effective_at<=? ORDER BY effective_at DESC LIMIT 1", mid, at.UTC().Format(time.RFC3339Nano)).Scan(&raw)
	if e == sql.ErrNoRows {
		return fallback, nil
	}
	if e != nil {
		return nil, e
	}
	var v PriceVersion
	if e = json.Unmarshal([]byte(raw), &v); e != nil {
		return nil, e
	}
	p := v.snapshot()
	return &p, nil
}

// Units stay rational until the final displayed decimal. Unknown dimensions
// prevent a full cost; partial is the subtotal of dimensions actually known.
func estimateUnits(u Usage, p *Price, partial bool) *string {
	counts := map[string]*int64{"input_token": u.Input, "output_token": u.Output, "cached_input_token": u.Cached, "cache_creation_token": u.CacheCreation}
	if u.Input != nil {
		n := *u.Input
		for _, c := range []*int64{u.Cached, u.CacheCreation} {
			if c != nil {
				if *c < 0 || *c > n {
					return nil
				}
				n -= *c
			}
		}
		counts["input_token"] = &n
	}
	total := new(big.Rat)
	known := false
	rates := map[string]bool{}
	for _, unit := range p.Units {
		rates[unit.Dimension] = true
		n := counts[unit.Dimension]
		if n == nil || *n < 0 {
			if !partial {
				return nil
			}
			continue
		}
		amount, _ := new(big.Rat).SetString(unit.Amount)
		per, _ := new(big.Rat).SetString(unit.Per)
		total.Add(total, new(big.Rat).Mul(new(big.Rat).Quo(amount, per), big.NewRat(*n, 1)))
		known = true
	}
	if !partial {
		for dim, n := range counts {
			if n != nil && *n > 0 && !rates[dim] {
				return nil
			}
		}
		if u.Input == nil || u.Output == nil {
			return nil
		}
	}
	if !known {
		return nil
	}
	v := accountingMoney(total)
	return &v
}

func priceTokenRate(p *Price, dimension string) string {
	if p == nil {
		return ""
	}
	for _, u := range p.Units {
		if u.Dimension == dimension {
			n, _ := new(big.Rat).SetString(u.Amount)
			d, _ := new(big.Rat).SetString(u.Per)
			return strings.TrimRight(strings.TrimRight(new(big.Rat).Mul(new(big.Rat).Quo(n, d), big.NewRat(1000000, 1)).FloatString(18), "0"), ".")
		}
	}
	return ""
}
