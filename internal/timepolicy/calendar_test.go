package timepolicy

import("testing";"time")

func calendar()Calendar{loc:=time.FixedZone("CST",8*3600);week:=map[time.Weekday]BusinessDay{};for _,day:=range []time.Weekday{time.Monday,time.Tuesday,time.Wednesday,time.Thursday,time.Friday}{week[day]=BusinessDay{Open:true,OpensAt:8*time.Hour,ClosesAt:17*time.Hour}};return Calendar{Location:loc,Weekdays:week,Days:map[string]BusinessDay{"2026-08-21":{Open:false,Reason:"maintenance"}}}}
func TestNextOpenRollsWeekendAndClosedDate(t *testing.T){c:=calendar();loc:=c.Location;start:=time.Date(2026,8,21,9,0,0,0,loc);next,err:=c.NextOpen(start);if err!=nil||!next.Equal(time.Date(2026,8,24,8,0,0,0,loc)){t.Fatalf("next=%v err=%v",next,err)}}
func TestAddBusinessDurationCrossesClosedDays(t *testing.T){c:=calendar();loc:=c.Location;start:=time.Date(2026,8,20,16,0,0,0,loc);end,err:=c.AddBusinessDuration(start,3*time.Hour);want:=time.Date(2026,8,24,10,0,0,0,loc);if err!=nil||!end.Equal(want){t.Fatalf("end=%v want=%v err=%v",end,want,err)}}
func TestDeadlineRollsAfterCutoff(t *testing.T){c:=calendar();loc:=c.Location;start:=time.Date(2026,8,19,18,0,0,0,loc);end,err:=c.Deadline(start,DeadlineRule{Name:"review",Duration:2*time.Hour,BusinessTime:true,Cutoff:16*time.Hour,RollAfterCutoff:true});want:=time.Date(2026,8,20,10,0,0,0,loc);if err!=nil||!end.Equal(want){t.Fatalf("end=%v want=%v err=%v",end,want,err)}}
func TestBusinessDurationClipsOpenHours(t *testing.T){c:=calendar();loc:=c.Location;period:=Period{StartsAt:time.Date(2026,8,19,7,0,0,0,loc),EndsAt:time.Date(2026,8,20,10,0,0,0,loc)};duration,err:=BusinessDuration(c,period);if err!=nil||duration!=11*time.Hour{t.Fatalf("duration=%v err=%v",duration,err)}}
