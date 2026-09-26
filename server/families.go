package main

// eBird's taxonomy API never localizes familyComName (verified empirically —
// see the note on EbirdClient.Taxonomy), so family names are translated here
// instead, keyed by the stable familyCode (not the English string, which
// could vary in casing/punctuation). There are only ~250 bird families
// worldwide; this table is seeded from every family seen across this
// project's test hotspots (Alicante, Ourense, Bruselas + a few Belgian
// hotspots) and grows as new ones show up. FamilyName falls back to the
// English name for any (code, locale) not yet covered — never an error.
var familyTranslations = map[string]map[string]string{
	"accipi1": {"es": "Águilas, Milanos y Gavilanes", "fr": "Aigles, Milans et Éperviers"},
	"acroce2": {"es": "Carriceros", "fr": "Rousserolles et Alliés"},
	"aegith2": {"es": "Mitos", "fr": "Mésanges à longue queue"},
	"alaudi1": {"es": "Alondras", "fr": "Alouettes"},
	"alcedi1": {"es": "Martines Pescadores", "fr": "Martins-pêcheurs"},
	"alcida1": {"es": "Álcidos", "fr": "Alcidés"},
	"anatid1": {"es": "Patos, Gansos y Cisnes", "fr": "Canards, Oies et Cygnes"},
	"apodid1": {"es": "Vencejos", "fr": "Martinets"},
	"ardeid1": {"es": "Garzas y Garcetas", "fr": "Hérons, Aigrettes et Butors"},
	"burhin2": {"es": "Alcaravanes", "fr": "Œdicnèmes"},
	"cacatu2": {"es": "Cacatúas", "fr": "Cacatoès"},
	"caprim2": {"es": "Chotacabras", "fr": "Engoulevents"},
	"certhi2": {"es": "Agateadores", "fr": "Grimpereaux"},
	"charad1": {"es": "Chorlitos y Avefrías", "fr": "Pluviers et Vanneaux"},
	"ciconi2": {"es": "Cigüeñas", "fr": "Cigognes"},
	"cistic2": {"es": "Cistícolas y Afines", "fr": "Cisticoles et Alliés"},
	"columb2": {"es": "Palomas y Tórtolas", "fr": "Pigeons et Tourterelles"},
	"corvid1": {"es": "Cuervos, Urracas y Arrendajos", "fr": "Corbeaux, Pies et Geais"},
	"cuculi1": {"es": "Cucos", "fr": "Coucous"},
	"emberi2": {"es": "Escribanos", "fr": "Bruants"},
	"estril1": {"es": "Estrildidas", "fr": "Astrilds et Alliés"},
	"falcon1": {"es": "Halcones", "fr": "Faucons"},
	"fringi1": {"es": "Pinzones", "fr": "Fringilles"},
	"gaviid1": {"es": "Colimbos", "fr": "Plongeons"},
	"haemat1": {"es": "Ostreros", "fr": "Huîtriers"},
	"hirund2": {"es": "Golondrinas y Aviones", "fr": "Hirondelles"},
	"hydrob1": {"es": "Paíños", "fr": "Océanites"},
	"laniid1": {"es": "Alcaudones", "fr": "Pies-grièches"},
	"larida1": {"es": "Gaviotas y Charranes", "fr": "Goélands, Mouettes et Sternes"},
	"locust5": {"es": "Buscarlas y Afines", "fr": "Locustelles et Alliés"},
	"meropi1": {"es": "Abejarucos", "fr": "Guêpiers"},
	"motaci1": {"es": "Lavanderas y Bisbitas", "fr": "Bergeronnettes et Pipits"},
	"muscic3": {"es": "Papamoscas y Petirrojos", "fr": "Gobemouches"},
	"orioli1": {"es": "Oropéndolas", "fr": "Loriots"},
	"pandio1": {"es": "Águila Pescadora", "fr": "Balbuzard"},
	"parida1": {"es": "Carboneros y Herrerillos", "fr": "Mésanges"},
	"passer4": {"es": "Gorriones", "fr": "Moineaux"},
	"phalac1": {"es": "Cormoranes", "fr": "Cormorans"},
	"phasia1": {"es": "Faisanes y Perdices", "fr": "Faisans et Perdrix"},
	"phoeni1": {"es": "Flamencos", "fr": "Flamants"},
	"phyllo4": {"es": "Mosquiteros", "fr": "Pouillots"},
	"picida1": {"es": "Pájaros Carpinteros", "fr": "Pics"},
	"podici1": {"es": "Somormujos y Zampullines", "fr": "Grèbes"},
	"procel3": {"es": "Pardelas y Petreles", "fr": "Puffins et Pétrels"},
	"prunel1": {"es": "Acentores", "fr": "Accenteurs"},
	"psitta3": {"es": "Loros", "fr": "Perroquets"},
	"psitta4": {"es": "Loros del Viejo Mundo", "fr": "Perruches et Perroquets de l'Ancien Monde"},
	"rallid1": {"es": "Rascones, Polluelas y Fochas", "fr": "Râles, Gallinules et Foulques"},
	"recurv1": {"es": "Cigüeñuelas y Avocetas", "fr": "Échasses et Avocettes"},
	"reguli1": {"es": "Reyezuelos", "fr": "Roitelets"},
	"remizi1": {"es": "Pájaros Moscones", "fr": "Rémiz"},
	"scolop2": {"es": "Correlimos y Agachadizas", "fr": "Bécasseaux et Limicoles"},
	"scotoc1": {"es": "Cetias y Afines", "fr": "Bouscarles et Alliés"},
	"sittid1": {"es": "Trepadores", "fr": "Sittelles"},
	"sterco1": {"es": "Págalos", "fr": "Labbes"},
	"strigi1": {"es": "Búhos y Lechuzas", "fr": "Hiboux et Chouettes"},
	"sturni1": {"es": "Estorninos", "fr": "Étourneaux"},
	"sulida1": {"es": "Alcatraces", "fr": "Fous"},
	"sylvii1": {"es": "Currucas", "fr": "Fauvettes"},
	"thresk1": {"es": "Ibis y Espátulas", "fr": "Ibis et Spatules"},
	"troglo1": {"es": "Chochines", "fr": "Troglodytes"},
	"turdid1": {"es": "Zorzales y Mirlos", "fr": "Grives et Merles"},
	"tytoni1": {"es": "Lechuzas", "fr": "Effraies"},
	"upupid1": {"es": "Abubillas", "fr": "Huppes"},
}

// FamilyName returns the family name in the given locale, falling back to
// englishName if the family or locale isn't in the table (e.g. "en" itself,
// or a family this table hasn't seen yet).
func FamilyName(familyCode, englishName, locale string) string {
	if byLocale, ok := familyTranslations[familyCode]; ok {
		if name, ok := byLocale[locale]; ok {
			return name
		}
	}
	return englishName
}
